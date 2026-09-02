// Package codex reaches the subscription on this machine by running the codex
// command line, and presents it as an api.Completer like everything else.
//
// The other two transports have the same shape of limit. A box runs out of
// turns, a free gateway answers 429 with fourteen hours on the header, and a
// run spends most of its wall clock waiting for one or the other to come back.
// Neither is a question of money and neither is quick.
//
// The subscription is paid for already and the CLI speaks to it from here, with
// no browser, no rented box and no key in an environment variable. So it is a
// third transport behind the same interface: the same prompt, the same gates
// deciding what is accepted, the same queue, reached by running a program and
// reading what it prints.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/tamnd/godev-vn-translator/api"
)

// Bin is the command. On PATH rather than an absolute path, because the CLI
// updates itself and a pinned path goes stale.
const Bin = "codex"

// DefaultTimeout is what one call is given before it is abandoned.
//
// Well over what a chunk takes. The full model measures about forty seconds on
// a chunk of six thousand characters and the cheap one about seventeen. The
// number here is for the case where the CLI is waiting on a login or on a
// network that is not there, which is a thing to fail out of rather than to
// hang on.
const DefaultTimeout = 10 * time.Minute

// Client runs the CLI. It satisfies api.Completer, so package route can hand it
// to the pool beside an HTTP client and the pool cannot tell them apart.
type Client struct {
	// Timeout bounds one call. Zero is DefaultTimeout.
	Timeout time.Duration
	// Exec runs the command and gives back what it wrote to standard output. It
	// is a field so a test can run this without the CLI, a subscription or a
	// network.
	Exec func(ctx context.Context, name string, args []string, stdin string) (string, error)
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *Client) run(ctx context.Context, args []string, stdin string) (string, error) {
	if c.Exec != nil {
		return c.Exec(ctx, Bin, args, stdin)
	}
	cmd := exec.CommandContext(ctx, Bin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	// The CLI reports a refusal, a rate limit and a model it does not serve on
	// standard output as a line of the stream, and standard error carries the
	// progress it prints for a person. So the output is worth reading even when
	// the exit status is not zero, and the parse below says what went wrong far
	// better than "exit status 1" does.
	if err != nil && len(out) == 0 {
		return "", err
	}
	return string(out), nil
}

// Complete asks the subscription one question.
//
// The system message and the user message are joined with a blank line rather
// than sent separately, because the CLI takes one prompt on standard input and
// has nowhere to put a system role. That is a real difference from the HTTP
// transports and it is the only one: the text the model sees is the same text,
// in the same order.
func (c *Client) Complete(ctx context.Context, request api.Request) (api.Response, error) {
	if strings.TrimSpace(request.Model) == "" {
		return api.Response{}, fmt.Errorf("codex: no model was named")
	}
	if strings.TrimSpace(request.Input) == "" {
		return api.Response{}, fmt.Errorf("codex: there is no question to ask")
	}
	prompt := strings.TrimSpace(request.Input)
	if instructions := strings.TrimSpace(request.Instructions); instructions != "" {
		prompt = instructions + "\n\n" + prompt
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	// exec, so nothing interactive is started. read-only, so a translation
	// cannot write to the disk of the machine that asked for it, which matters
	// because the thing being translated is a file on that disk. The git check
	// is skipped because the question has nothing to do with the directory it
	// happens to be asked in.
	args := []string{"exec", "-m", request.Model, "--json", "--skip-git-repo-check", "-s", "read-only", "-"}
	started := time.Now()
	out, err := c.run(ctx, args, prompt)
	if err != nil {
		return api.Response{}, fmt.Errorf("codex: %w", err)
	}
	result := readStream(out)
	if result.why != "" {
		return api.Response{}, fmt.Errorf("codex: %s", result.why)
	}
	if strings.TrimSpace(result.text) == "" {
		return api.Response{}, fmt.Errorf("codex: answered with nothing")
	}
	model := result.model
	if model == "" {
		model = request.Model
	}
	return api.Response{
		Model:   model,
		Text:    strings.TrimSpace(result.text),
		Usage:   result.usage.Normalized(),
		Elapsed: time.Since(started),
	}, nil
}

type streamResult struct {
	text  string
	model string
	usage api.Usage
	why   string
}

// readStream picks the answer out of what the CLI printed.
//
// The CLI writes one JSON object a line: the thread starting, the turn
// starting, each item it completed, and the turn completing with what the turn
// cost. The answer is the last completed item of type agent_message, the last
// rather than the first because a turn that thinks aloud completes more than one
// and the last is the one addressed to whoever asked.
//
// A line that does not parse is passed over rather than being an error. The CLI
// prints its own notices on this stream from time to time, and a notice is not
// a reason to throw away an answer sitting three lines below it.
//
// The first reason is kept and not the last. A turn that fails prints what the
// endpoint said and then prints that the turn failed, and the first of those is
// the sentence somebody can act on: the model is not one this account serves, or
// the account is out of turns until Tuesday. The second is "nope".
func readStream(out string) streamResult {
	var result streamResult
	scanner := bufio.NewScanner(strings.NewReader(out))
	// A chunk of a page and its translation go on one line of this stream, and
	// the default is sixty four kilobytes.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Item    struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage struct {
				Input  int `json:"input_tokens"`
				Cached int `json:"cached_input_tokens"`
				Output int `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch {
		case event.Type == "item.completed" && event.Item.Type == "agent_message":
			result.text = event.Item.Text
		case event.Type == "turn.completed":
			result.usage = api.Usage{
				InputTokens:       event.Usage.Input,
				CachedInputTokens: event.Usage.Cached,
				OutputTokens:      event.Usage.Output,
			}
		case event.Type == "error" && result.why == "":
			result.why = reason(event.Message)
		case event.Type == "turn.failed" && result.why == "":
			result.why = reason(event.Error.Message)
		}
	}
	// A turn that failed and then answered is not a failure. The CLI retries a
	// call it can retry, so an error line early in the stream can be followed by
	// a perfectly good answer, and only an error with nothing after it is worth
	// reporting.
	if result.text != "" {
		result.why = ""
	}
	return result
}

// reason pulls the sentence out of the CLI's error, which is a JSON object
// printed inside the message field of another JSON object.
//
// The plain message is kept when it does not parse, since a sentence is what
// the caller is going to log either way and half a sentence is better than a
// blob of punctuation.
func reason(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "the run failed and said nothing"
	}
	var inner struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(message), &inner) == nil && inner.Error.Message != "" {
		return condense(inner.Error.Message)
	}
	return condense(message)
}

func condense(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}
