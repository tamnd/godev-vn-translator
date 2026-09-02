package codex

import (
	"context"
	"strings"
	"testing"

	"github.com/tamnd/godev-vn-translator/api"
)

// fake stands in for the CLI, so these tests run with no subscription, no
// network and no login.
func fake(out string, record *[]string, stdin *string) func(context.Context, string, []string, string) (string, error) {
	return func(_ context.Context, _ string, args []string, in string) (string, error) {
		if record != nil {
			*record = args
		}
		if stdin != nil {
			*stdin = in
		}
		return out, nil
	}
}

// The stream below is what the CLI prints: one JSON object a line, the answer
// arriving as a completed item and the cost arriving at the end of the turn.
const stream = `{"type":"thread.started","thread_id":"t1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"type":"reasoning","text":"thinking"}}
{"type":"item.completed","item":{"type":"agent_message","text":"Bo nho duoc cap phat."}}
{"type":"turn.completed","usage":{"input_tokens":1840,"cached_input_tokens":1792,"output_tokens":95}}
`

func TestComplete(t *testing.T) {
	var args []string
	var stdin string
	client := &Client{Exec: fake(stream, &args, &stdin)}
	response, err := client.Complete(context.Background(), api.Request{
		Model:        "gpt-5.4",
		Instructions: "Translate into Vietnamese.",
		Input:        "Memory is allocated.",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "Bo nho duoc cap phat." {
		t.Errorf("Text = %q", response.Text)
	}
	want := api.Usage{InputTokens: 1840, CachedInputTokens: 1792, OutputTokens: 95, TotalTokens: 1935}
	if response.Usage != want {
		t.Errorf("Usage = %+v, want %+v", response.Usage, want)
	}

	// The CLI has nowhere to put a system role, so the instructions and the
	// input are joined into one prompt. The text the model sees has to be the
	// same text in the same order as the HTTP transports send.
	if stdin != "Translate into Vietnamese.\n\nMemory is allocated." {
		t.Errorf("stdin = %q", stdin)
	}
	// read-only, because the thing being translated is a file on this disk.
	joined := strings.Join(args, " ")
	for _, want := range []string{"exec", "-m gpt-5.4", "--json", "-s read-only", "--skip-git-repo-check"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %v are missing %q", args, want)
		}
	}
}

// A turn that thinks aloud completes more than one message. The last one is the
// one addressed to whoever asked.
func TestLastMessageWins(t *testing.T) {
	out := `{"type":"item.completed","item":{"type":"agent_message","text":"first"}}
{"type":"item.completed","item":{"type":"agent_message","text":"second"}}
`
	response, err := (&Client{Exec: fake(out, nil, nil)}).Complete(context.Background(),
		api.Request{Model: "gpt-5.4", Input: "hi"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "second" {
		t.Errorf("Text = %q, want the last message", response.Text)
	}
}

// The CLI retries what it can retry, so an error line early in the stream can
// be followed by a perfectly good answer. Reporting the error would throw the
// answer away.
func TestErrorFollowedByAnAnswerIsNotAFailure(t *testing.T) {
	out := `{"type":"error","message":"stream disconnected before completion"}
{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}
`
	response, err := (&Client{Exec: fake(out, nil, nil)}).Complete(context.Background(),
		api.Request{Model: "gpt-5.4", Input: "hi"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "ok" {
		t.Errorf("Text = %q", response.Text)
	}
}

// The first reason is the one somebody can act on. The second is "nope".
func TestFirstReasonIsKept(t *testing.T) {
	out := `{"type":"error","message":"{\"error\":{\"message\":\"You've hit your usage limit.\"}}"}
{"type":"turn.failed","error":{"message":"turn failed"}}
`
	_, err := (&Client{Exec: fake(out, nil, nil)}).Complete(context.Background(),
		api.Request{Model: "gpt-5.4", Input: "hi"})
	if err == nil {
		t.Fatal("a failed turn was accepted")
	}
	if !strings.Contains(err.Error(), "usage limit") {
		t.Errorf("error = %v, want the sentence the endpoint sent", err)
	}
}

// A turn that produced nothing is not an empty translation. Writing one into
// _content_vi would shadow the English file and serve a blank page.
func TestEmptyIsAnError(t *testing.T) {
	out := `{"type":"turn.completed","usage":{"input_tokens":9,"output_tokens":0}}` + "\n"
	if _, err := (&Client{Exec: fake(out, nil, nil)}).Complete(context.Background(),
		api.Request{Model: "gpt-5.4", Input: "hi"}); err == nil {
		t.Fatal("an empty turn was accepted")
	}
}

// The CLI prints its own notices on this stream from time to time, and a notice
// is not a reason to throw away an answer sitting three lines below it.
func TestGarbageLinesArePassedOver(t *testing.T) {
	out := "warning: a new version is available\n" +
		`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}` + "\n"
	response, err := (&Client{Exec: fake(out, nil, nil)}).Complete(context.Background(),
		api.Request{Model: "gpt-5.4", Input: "hi"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "ok" {
		t.Errorf("Text = %q", response.Text)
	}
}

// A chunk of a page and its translation go on one line of this stream, well
// past the scanner's default of sixty four kilobytes.
func TestALongLineIsRead(t *testing.T) {
	long := strings.Repeat("Bo nho duoc cap phat. ", 20000)
	out := `{"type":"item.completed","item":{"type":"agent_message","text":"` + long + `"}}` + "\n"
	response, err := (&Client{Exec: fake(out, nil, nil)}).Complete(context.Background(),
		api.Request{Model: "gpt-5.4", Input: "hi"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(response.Text) < len(long)-1 {
		t.Errorf("read %d bytes of a %d byte answer", len(response.Text), len(long))
	}
}

func TestRefusesAnIncompleteRequest(t *testing.T) {
	client := &Client{Exec: fake(stream, nil, nil)}
	if _, err := client.Complete(context.Background(), api.Request{Input: "hi"}); err == nil {
		t.Error("a request with no model was accepted")
	}
	if _, err := client.Complete(context.Background(), api.Request{Model: "gpt-5.4"}); err == nil {
		t.Error("a request with no input was accepted")
	}
}
