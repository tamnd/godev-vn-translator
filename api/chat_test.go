package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The stream below is the shape chatgpt-tool serve returns: a first chunk that
// carries the id and model and no content, content deltas, then a final chunk
// with empty choices and the usage on it.
const stream = `data: {"id":"chatcmpl-1","model":"gpt-5","choices":[{"delta":{"role":"assistant"}}]}

data: {"id":"chatcmpl-1","model":"gpt-5","choices":[{"delta":{"content":"Bo nho"}}]}

data: {"id":"chatcmpl-1","model":"gpt-5","choices":[{"delta":{"content":" duoc cap phat."}}]}

data: {"id":"chatcmpl-1","model":"gpt-5","choices":[],"usage":{"prompt_tokens":1840,"completion_tokens":95,"total_tokens":1935,"prompt_tokens_details":{"cached_tokens":1792},"completion_tokens_details":{"reasoning_tokens":40}}}

data: [DONE]
`

func TestCompleteStream(t *testing.T) {
	var got chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("request is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	defer server.Close()

	client := &Client{URL: server.URL, APIKey: "key"}
	response, err := client.Complete(context.Background(), Request{
		Model: "gpt-5", Instructions: "Translate into Vietnamese.", Input: "Memory is allocated.",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if want := "Bo nho duoc cap phat."; response.Text != want {
		t.Errorf("Text = %q, want %q", response.Text, want)
	}
	if response.Model != "gpt-5" || response.ID != "chatcmpl-1" {
		t.Errorf("id/model = %q/%q", response.ID, response.Model)
	}
	// The usage rides on the last chunk, which has no choices. A reader that
	// stops at the first chunk without content would report none of it.
	want := Usage{InputTokens: 1840, CachedInputTokens: 1792, OutputTokens: 95, ReasoningTokens: 40, TotalTokens: 1935}
	if response.Usage != want {
		t.Errorf("Usage = %+v, want %+v", response.Usage, want)
	}
	if !got.Stream || !got.StreamOptions.IncludeUsage {
		t.Errorf("did not ask for a stream with usage: %+v", got)
	}
	if got.PromptCacheKey == "" {
		t.Error("no prompt cache key, so the translation prompt is paid for on every chunk")
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" {
		t.Errorf("messages = %+v", got.Messages)
	}
}

// A server that ignores stream: true and answers with plain JSON is not worth
// arguing with, so the client reads that too.
func TestCompleteJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c2","model":"gpt-5","choices":[{"message":{"content":"ok"}}],
			"usage":{"prompt_tokens":9,"completion_tokens":1}}`)
	}))
	defer server.Close()

	response, err := (&Client{URL: server.URL}).Complete(context.Background(), Request{Model: "gpt-5", Input: "hi"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "ok" {
		t.Errorf("Text = %q", response.Text)
	}
	// No total came back, so it is the sum rather than zero.
	if response.Usage.TotalTokens != 10 {
		t.Errorf("TotalTokens = %d, want 10", response.Usage.TotalTokens)
	}
}

// A 200 with an empty body is worse than an error, because it looks like an
// answer. An empty file written into _content_vi shadows the English one and
// serves a blank page.
func TestEmptyIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c3\",\"choices\":[]}\n\ndata: [DONE]\n")
	}))
	defer server.Close()

	if _, err := (&Client{URL: server.URL}).Complete(context.Background(), Request{Model: "gpt-5", Input: "hi"}); err == nil {
		t.Fatal("an empty stream was accepted")
	}
}

func TestRetryThenSucceed(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"usage_limit_reached"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c4","choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	var slept time.Duration
	client := &Client{URL: server.URL, MaxRetries: 2,
		Sleep: func(context.Context, time.Duration) error { return nil }}
	client.Sleep = func(_ context.Context, d time.Duration) error { slept += d; return nil }
	response, err := client.Complete(context.Background(), Request{Model: "gpt-5", Input: "hi"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "ok" || calls != 2 {
		t.Errorf("Text = %q after %d calls", response.Text, calls)
	}
	// The header said two seconds, so it waited two seconds rather than the
	// backoff it would have invented.
	if slept != 2*time.Second {
		t.Errorf("slept %s, want 2s from Retry-After", slept)
	}
}

// A 400 is the model or the request, not the transport. Retrying it just burns
// the same minute again on a host that is answering perfectly well.
func TestNoRetryOnBadRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"model not found"}}`)
	}))
	defer server.Close()

	client := &Client{URL: server.URL, MaxRetries: 3,
		Sleep: func(context.Context, time.Duration) error { return nil }}
	_, err := client.Complete(context.Background(), Request{Model: "gpt-5", Input: "hi"})
	if err == nil {
		t.Fatal("a 400 was accepted")
	}
	if calls != 1 {
		t.Errorf("called %d times, want 1", calls)
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error lost the provider message: %v", err)
	}
}

// A provider that names a wait longer than we will sit out is telling us to go
// elsewhere. Sitting there is what makes a whole run stall on one bad host.
func TestGivesUpOnLongRetryAfter(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := &Client{URL: server.URL, MaxRetries: 3, MaxRetryDelay: time.Minute,
		Sleep: func(context.Context, time.Duration) error { return errors.New("slept, and should not have") }}
	if _, err := client.Complete(context.Background(), Request{Model: "gpt-5", Input: "hi"}); err == nil {
		t.Fatal("a 429 was accepted")
	}
	if calls != 1 {
		t.Errorf("called %d times, want 1", calls)
	}
}

func TestUsageNormalized(t *testing.T) {
	for _, c := range []struct {
		name string
		in   Usage
		want Usage
	}{
		{
			"cache read larger than input, which is malformed detail",
			Usage{InputTokens: 100, CachedInputTokens: 400, OutputTokens: 10},
			Usage{InputTokens: 100, CachedInputTokens: 100, OutputTokens: 10, TotalTokens: 110},
		},
		{
			"reasoning is part of output, never more than it",
			Usage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 9},
			Usage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 5, TotalTokens: 15},
		},
		{
			"a total that came back is kept, not recomputed",
			Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 99},
			Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 99},
		},
	} {
		if got := c.in.Normalized(); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestErrorMessage(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"error":{"message":"usage_limit_reached","type":"rate_limit"}}`, "usage_limit_reached"},
		{`{"message":"no ChatGPT response found"}`, "no ChatGPT response found"},
		// chatgpt-tool relays some upstream failures as plain text. Reporting
		// nothing for those would hide the only clue there is.
		{"502 Bad Gateway\n\nupstream closed", "502 Bad Gateway upstream closed"},
	} {
		if got := ErrorMessage([]byte(c.body)); got != c.want {
			t.Errorf("ErrorMessage(%q) = %q, want %q", c.body, got, c.want)
		}
	}
}
