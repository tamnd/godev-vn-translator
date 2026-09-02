package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client speaks POST /v1/chat/completions, streaming.
//
// Streaming is not decoration. A six thousand character chunk of ref/mod.md
// takes minutes to come back through a browser session, and a non-streaming
// request holds one connection open with nothing on it for that whole time,
// which is exactly the shape an idle timeout somewhere in the middle kills. The
// stream also carries usage on its last chunk, which is how a truncated answer
// gets noticed before it is written to a file.
type Client struct {
	URL        string
	APIKey     string
	HTTPClient *http.Client
	// MaxRetries is retries inside one call, for a failure that looks like it
	// will pass. Failing over to another host is the router's job, not this.
	MaxRetries    int
	MaxRetryDelay time.Duration
	UserAgent     string
	// Sleep is the wait between attempts, replaceable so a test does not.
	Sleep func(context.Context, time.Duration) error
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Stream         bool          `json:"stream"`
	StreamOptions  streamOptions `json:"stream_options"`
	PromptCacheKey string        `json:"prompt_cache_key,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		CacheReadTokens  int `json:"cache_read_input_tokens"`
		PromptDetails    struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) Complete(ctx context.Context, request Request) (Response, error) {
	if strings.TrimSpace(c.URL) == "" {
		return Response{}, errors.New("chat completions URL is empty")
	}
	if strings.TrimSpace(request.Model) == "" {
		return Response{}, errors.New("model is empty")
	}
	if strings.TrimSpace(request.Input) == "" {
		return Response{}, errors.New("input is empty")
	}
	messages := make([]chatMessage, 0, 2)
	if strings.TrimSpace(request.Instructions) != "" {
		messages = append(messages, chatMessage{Role: "system", Content: request.Instructions})
	}
	messages = append(messages, chatMessage{Role: "user", Content: request.Input})
	payload, err := json.Marshal(chatRequest{
		Model:          request.Model,
		Messages:       messages,
		Stream:         true,
		StreamOptions:  streamOptions{IncludeUsage: true},
		PromptCacheKey: cacheKey(request.Instructions),
	})
	if err != nil {
		return Response{}, fmt.Errorf("encode chat request: %w", err)
	}

	started := time.Now()
	var last error
	for attempt := 0; attempt <= max(0, c.MaxRetries); attempt++ {
		response, retryAfter, retry, err := c.do(ctx, payload)
		if err == nil {
			response.Elapsed = time.Since(started)
			return response, nil
		}
		last = err
		if !retry || attempt == max(0, c.MaxRetries) {
			break
		}
		// A provider that names a wait longer than we are willing to sit out is
		// telling us to go elsewhere. Give the router the chance.
		if c.MaxRetryDelay > 0 && retryAfter > c.MaxRetryDelay {
			break
		}
		if retryAfter <= 0 {
			retryAfter = backoff(attempt)
		}
		if err := c.sleep(ctx, retryAfter); err != nil {
			return Response{}, err
		}
	}
	return Response{}, last
}

func (c *Client) do(ctx context.Context, payload []byte) (Response, time.Duration, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(payload))
	if err != nil {
		return Response{}, 0, false, fmt.Errorf("create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, 0, false, ctx.Err()
		}
		return Response{}, 0, true, fmt.Errorf("call chat completions: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		after := parseRetryAfter(resp.Header.Get("Retry-After"))
		return Response{}, after, retry, fmt.Errorf("chat completions returned %s: %s%s",
			resp.Status, ErrorMessage(raw), retryAfterSuffix(after))
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		response, err := parseStream(resp.Body)
		if err != nil {
			return Response{}, 0, true, err
		}
		return response, 0, false, nil
	}
	// Not every compatible server honours stream: true. Reading the plain JSON
	// body is cheaper than arguing with it.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return Response{}, 0, true, fmt.Errorf("read chat response: %w", err)
	}
	response, err := parseJSON(raw)
	if err != nil {
		return Response{}, 0, false, err
	}
	return response, 0, false, nil
}

func parseStream(reader io.Reader) (Response, error) {
	scanner := bufio.NewScanner(reader)
	// A translated chunk is tens of kilobytes and arrives in small deltas, but
	// a server that batches can put the lot in one data: line, so the ceiling
	// is generous.
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	var text strings.Builder
	var response Response
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var chunk chatChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return Response{}, fmt.Errorf("decode chat stream: %w", err)
		}
		if chunk.Error != nil {
			return Response{}, fmt.Errorf("chat stream error: %s", chunk.Error.Message)
		}
		if response.ID == "" {
			response.ID = chunk.ID
		}
		if chunk.Model != "" {
			response.Model = chunk.Model
		}
		for _, choice := range chunk.Choices {
			text.WriteString(choice.Delta.Content)
			text.WriteString(choice.Message.Content)
		}
		setUsage(&response, chunk)
	}
	if err := scanner.Err(); err != nil {
		return Response{}, fmt.Errorf("read chat stream: %w", err)
	}
	response.Text = strings.TrimSpace(text.String())
	if response.Text == "" {
		return Response{}, errors.New("chat completions returned no text")
	}
	return response, nil
}

func parseJSON(raw []byte) (Response, error) {
	var chunk chatChunk
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return Response{}, fmt.Errorf("decode chat response: %w", err)
	}
	if chunk.Error != nil {
		return Response{}, fmt.Errorf("chat completions error: %s", chunk.Error.Message)
	}
	response := Response{ID: chunk.ID, Model: chunk.Model}
	var text strings.Builder
	for _, choice := range chunk.Choices {
		text.WriteString(choice.Message.Content)
		text.WriteString(choice.Delta.Content)
	}
	setUsage(&response, chunk)
	response.Text = strings.TrimSpace(text.String())
	if response.Text == "" {
		return Response{}, errors.New("chat completions returned no text")
	}
	return response, nil
}

func setUsage(response *Response, chunk chatChunk) {
	if chunk.Usage == nil {
		return
	}
	cached := chunk.Usage.PromptDetails.CachedTokens
	if cached == 0 {
		cached = chunk.Usage.CacheReadTokens
	}
	response.Usage = Usage{
		InputTokens:       chunk.Usage.PromptTokens,
		CachedInputTokens: cached,
		OutputTokens:      chunk.Usage.CompletionTokens,
		ReasoningTokens:   chunk.Usage.CompletionDetails.ReasoningTokens,
		TotalTokens:       chunk.Usage.TotalTokens,
	}.Normalized()
}

func (c *Client) sleep(ctx context.Context, duration time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ErrorMessage pulls the human sentence out of an error body. Falling back to
// the raw body matters: chatgpt-tool relays some upstream failures as plain
// text, and reporting an empty detail for those would hide the only clue.
func ErrorMessage(raw []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		if text := strings.TrimSpace(envelope.Error.Message); text != "" {
			return condense(text)
		}
		if text := strings.TrimSpace(envelope.Message); text != "" {
			return condense(text)
		}
	}
	return condense(string(raw))
}

// cacheKey groups calls that share a system prompt.
//
// The translation instructions run to a couple of thousand tokens once the
// rules and the glossary are in them, and they are the same on every chunk of
// every file in a run. Naming that prefix is the difference between paying for
// it once and paying for it several thousand times.
//
// The glossary block is cut down per chunk, so two chunks that mention
// different terms get different keys and that is correct. The point is that all
// 178 chunks of ref/mod.md share one.
func cacheKey(instructions string) string {
	if instructions == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(instructions))
	return "godev-" + hex.EncodeToString(sum[:8])
}
