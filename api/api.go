// Package api is the model transport: one request shape, one response shape,
// and a client that speaks the OpenAI chat completions wire.
//
// There is one wire here and three things on the other end of it. Locally the
// call goes to a codex subscription through the codex command line. On the
// fleet it goes over an ssh tunnel to chatgpt-tool serve on server1, server2 or
// server3, each of which fronts a pool of ChatGPT web sessions and answers
// POST /v1/chat/completions. A plain OpenAI compatible gateway is the third
// case and needs no adapter at all. Nothing above package route knows which one
// answered, which is the whole point: the translator asks for a completion and
// gets one.
//
// This is the same shape as the transport in tamnd/bourbaki-solver, which has
// been running against the same fleet for long enough to have found the edges.
// The parts that look over careful, the plain JSON fallback and the 32MB stream
// buffer, are there because a compatible server did the thing they handle.
package api

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Completer is one model call. It is an interface so a test can answer without
// a server and so the pool can wrap it.
type Completer interface {
	Complete(ctx context.Context, request Request) (Response, error)
}

// Request is one call to a model.
type Request struct {
	Model string
	// Instructions is the system message. On a translation run it is the same
	// two thousand characters on every one of several thousand calls, so it is
	// also what the prompt cache key is derived from.
	Instructions string
	Input        string
}

// Response is what came back, plus enough about where it came from to write an
// honest report.
type Response struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	// Route names what served the call. A page assembled from four chunks that
	// went to three different routes has to be able to say so.
	Route   string        `json:"route,omitempty"`
	Text    string        `json:"text"`
	Usage   Usage         `json:"usage"`
	Elapsed time.Duration `json:"elapsed"`
}

// Usage is the token accounting the provider returned. InputTokens counts
// cached reads as well. ReasoningTokens is a part of OutputTokens and must not
// be added to it.
//
// None of these routes bills anything, so the numbers buy no cost estimate.
// They are here because they are the only measure of how much work a chunk took
// from outside, and a chunk that comes back with a tenth of the output tokens
// its neighbours used is a chunk that got truncated. L03 catches that on the
// finished file, and this catches it a step earlier.
type Usage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	ReasoningTokens   int `json:"reasoning_tokens"`
	TotalTokens       int `json:"total_tokens"`
}

// Normalized fills in totals a compatible proxy left out and stops malformed
// cache detail from producing a negative count of uncached input.
func (u Usage) Normalized() Usage {
	u.InputTokens = max(0, u.InputTokens)
	u.OutputTokens = max(0, u.OutputTokens)
	u.CachedInputTokens = min(max(0, u.CachedInputTokens), u.InputTokens)
	u.ReasoningTokens = min(max(0, u.ReasoningTokens), u.OutputTokens)
	if u.TotalTokens <= 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	return u
}

// Add sums two accountings, for a report that covers a whole run.
func (u Usage) Add(other Usage) Usage {
	u.InputTokens += other.InputTokens
	u.CachedInputTokens += other.CachedInputTokens
	u.OutputTokens += other.OutputTokens
	u.ReasoningTokens += other.ReasoningTokens
	u.TotalTokens += other.TotalTokens
	return u
}

// parseRetryAfter reads the header, which a provider may write as a number of
// seconds or as an HTTP date.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(when))
	}
	return 0
}

func retryAfterSuffix(delay time.Duration) string {
	if delay <= 0 {
		return ""
	}
	return fmt.Sprintf(" (retry after %s)", delay)
}

// backoff doubles from a second to thirty, with jitter so a set of workers that
// all failed on the same dead tunnel do not all come back at once.
func backoff(attempt int) time.Duration {
	base := min(30*time.Second, time.Second<<min(attempt, 5))
	return base + time.Duration(rand.IntN(500))*time.Millisecond
}

// condense makes a provider message fit one log line and one table cell. A
// gateway that is down answers with an HTML error page, and a detail field
// holding twenty lines of markup turns the doctor table into something nobody
// can read.
func condense(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}
