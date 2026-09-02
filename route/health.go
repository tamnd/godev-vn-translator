package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/godev-vn-translator/api"
	"github.com/tamnd/godev-vn-translator/codex"
)

// State is what a probe found. The distinctions exist because the remedies
// differ: out of quota is a wait, a rejected key is a login, a missing model is
// an edit to the route file, and a dead tunnel is an ssh to bring back up.
type State string

const (
	StateLive         State = "live"
	StateQuota        State = "quota"
	StateUnauthorized State = "unauthorized"
	StateUnreachable  State = "unreachable"
	StateBroken       State = "broken"
	StateGone         State = "gone"
	StateUnknown      State = "unknown"
)

// Usable reports whether a route in this state is worth sending work to. An
// unprobed route counts, because the first call is itself a probe.
func (s State) Usable() bool { return s == StateLive || s == StateUnknown }

// Health is one probe result.
type Health struct {
	Route     string        `json:"route"`
	State     State         `json:"state"`
	Latency   time.Duration `json:"latency"`
	Detail    string        `json:"detail"`
	ResetsAt  time.Time     `json:"resets_at,omitzero"`
	Model     string        `json:"model,omitempty"`
	CheckedAt time.Time     `json:"checked_at"`

	// Transport is what the host says it is fronting, "browser" or "auto".
	Transport string `json:"transport,omitempty"`
	// Verified is how many ChatGPT sessions the pool has logged in. It is the
	// number that silently drops when a session expires, and a host at zero
	// answers every call with a refusal that looks like a model failure.
	Verified int `json:"verified,omitempty"`
	// Declared is the concurrency the host announces, which is not always the
	// one the route file assumes.
	Declared int `json:"declared,omitempty"`
	// Catalogue is the model list the host advertises.
	Catalogue []string `json:"catalogue,omitempty"`
	// Drift is set when the configured model is absent from the catalogue.
	Drift string `json:"drift,omitempty"`
	// Answered is the model that came back on a deep probe, which is not always
	// the one that was asked for. An account can be moved down between two runs
	// and neither the route file nor the catalogue can see it: both say what is
	// on offer, and this says what arrived.
	Answered string `json:"answered,omitempty"`
}

// Downgraded reports that the route answered on a model other than the one it
// was asked for.
//
// Only a deep probe can know, since it is the only one that asks a question. A
// shallow probe leaves Answered empty and this is false, which is the honest
// answer: not knowing is not the same as knowing it is fine.
func (h Health) Downgraded() bool {
	return h.Answered != "" && h.Model != "" && h.Answered != h.Model
}

// Signal is a classified provider response.
type Signal struct {
	State    State
	Detail   string
	ResetsAt time.Time
}

// quotaMarkers are copied from live bodies rather than guessed. A ChatGPT web
// session announces its daily limit in more than one way depending on which
// surface refused.
var quotaMarkers = []string{
	"usage_limit_reached",
	"rate_limit_exceeded",
	"too many requests",
	"quota",
	// The codex CLI writes its limit as a sentence for a person rather than as
	// a code, and a deep probe on a spent subscription reads exactly like this:
	// "You've hit your usage limit. Upgrade to Pro ... or try again at Sep 7th,
	// 2026 9:52 AM." Without this the route is filed as broken, which earns it
	// a short cooldown and a retry every few minutes for the five days it has
	// left to run.
	"usage limit",
	"out of credits",
}

var goneMarkers = []string{
	"is not supported",
	"model_not_found",
	"unknown model",
}

// Classify maps a provider response to a state. Order matters: a body naming a
// missing model is a route file problem even when it arrives with a status that
// would otherwise read as something else.
func Classify(status int, body string, err error) Signal {
	if err != nil {
		return ClassifyError(err)
	}
	lower := strings.ToLower(body)
	switch {
	case containsAny(lower, goneMarkers):
		return Signal{State: StateGone, Detail: message(body, "model is not served here")}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Signal{State: StateUnauthorized, Detail: message(body, "credential rejected")}
	case status == http.StatusTooManyRequests || containsAny(lower, quotaMarkers):
		return Signal{State: StateQuota, Detail: message(body, "out of quota"), ResetsAt: resetsFrom(body)}
	case strings.Contains(lower, "no chatgpt response found"):
		// The browser transport reached the page and found nothing on it. It is
		// a real failure of this host, not of the request, and it clears.
		return Signal{State: StateBroken, Detail: "no ChatGPT response found"}
	case status >= 400:
		return Signal{State: StateBroken, Detail: message(body, fmt.Sprintf("host returned %d", status))}
	case strings.TrimSpace(body) == "":
		// A 200 with nothing in it is worse than an error, because it looks
		// like an answer. It must never reach the caller as a candidate.
		return Signal{State: StateBroken, Detail: "empty response"}
	}
	return Signal{State: StateLive, Detail: "ok"}
}

// ClassifyError reads a Go-side error. The api client folds the upstream status
// and body into its error text, so this covers both a dead tunnel and a relayed
// provider message.
func ClassifyError(err error) Signal {
	if errors.Is(err, context.Canceled) {
		return Signal{State: StateUnknown, Detail: err.Error()}
	}
	text := condense(err.Error())
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, goneMarkers):
		return Signal{State: StateGone, Detail: text}
	case containsAny(lower, []string{"401", "403", "unauthorized", "forbidden", "invalid_api_key"}):
		return Signal{State: StateUnauthorized, Detail: text}
	case containsAny(lower, quotaMarkers) || strings.Contains(lower, "429"):
		return Signal{State: StateQuota, Detail: text, ResetsAt: resetsFrom(text)}
	case errors.Is(err, context.DeadlineExceeded) ||
		containsAny(lower, []string{"no such host", "connection refused", "dial tcp", "tls:", "i/o timeout", "timeout", "eof"}):
		// connection refused on a loopback port is the ordinary shape of a
		// tunnel that died, which is why it is unreachable and not broken.
		return Signal{State: StateUnreachable, Detail: text}
	}
	return Signal{State: StateBroken, Detail: text}
}

func containsAny(lower string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

var (
	// The quotes are optional because this also runs over an error string that
	// folded a JSON body into a sentence.
	resetsAtPattern   = regexp.MustCompile(`"?resets_at"?\s*[:= ]\s*(\d+)`)
	retryDelayPattern = regexp.MustCompile(`(?i)"?retry(?:_delay|_after|-after|afterseconds)"?\s*[:=]\s*"?(\d+)`)
	// The codex CLI names the hour in words: "try again at Sep 7th, 2026 9:52
	// AM". No zone, so it is the local one, which is right because the CLI is
	// running on this machine.
	tryAgainPattern = regexp.MustCompile(`(?i)try again at ([A-Z][a-z]{2} \d{1,2})(?:st|nd|rd|th)?, (\d{4}) (\d{1,2}:\d{2} ?[AP]M)`)
)

// resetsFrom digs the reset instant out of whatever shape the host used. A host
// that offers nothing leaves it zero, and a caller must read a zero instant as
// unknown rather than as the epoch.
func resetsFrom(body string) time.Time {
	if match := resetsAtPattern.FindStringSubmatch(body); match != nil {
		if seconds, err := strconv.ParseInt(match[1], 10, 64); err == nil && seconds > 0 {
			return time.Unix(seconds, 0).UTC()
		}
	}
	if match := retryDelayPattern.FindStringSubmatch(body); match != nil {
		if seconds, err := strconv.Atoi(match[1]); err == nil && seconds > 0 {
			return time.Now().UTC().Add(time.Duration(seconds) * time.Second)
		}
	}
	if match := tryAgainPattern.FindStringSubmatch(body); match != nil {
		text := match[1] + ", " + match[2] + " " + strings.ToUpper(strings.ReplaceAll(match[3], " ", ""))
		if when, err := time.ParseInLocation("Jan 2, 2006 3:04PM", text, time.Local); err == nil {
			return when.UTC()
		}
	}
	return time.Time{}
}

// message pulls a human sentence out of a body, falling back to a description
// when the body is not the shape we expected.
func message(body, fallback string) string {
	if text := api.ErrorMessage([]byte(body)); text != "" {
		return text
	}
	return fallback
}

func condense(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}

// Prober runs health checks.
//
// The check is deliberately not a model call. The measured round trip to
// server3 for nine tokens is 151 seconds, so probing three hosts that way costs
// eight minutes and would make doctor useless as a cron guard. GET /v1/health
// answers in milliseconds and says the thing that actually goes wrong, which is
// that the pool's sessions have logged themselves out. Deep asks for the model
// call as well, for when the question is whether the whole path works.
type Prober struct {
	HTTPClient *http.Client
	Timeout    time.Duration
	Deep       bool
	Now        func() time.Time
	// Look says where a program is, and is exec.LookPath unless a test says
	// otherwise. It is the whole of what a route with a command can be asked
	// without running the thing.
	Look func(name string) (string, error)
}

// command is the shallow probe for a route that is a program on this machine.
//
// Being on PATH is the whole of it. Whether the subscription behind it is
// signed in and has turns left is not something that can be asked without
// spending one, which is what the deep probe is for.
func (p Prober) command(_ context.Context, value Route) Signal {
	look := p.Look
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(value.Command)
	if err != nil {
		return Signal{State: StateUnreachable,
			Detail: fmt.Sprintf("%s is not on PATH on this machine: %v", value.Command, err)}
	}
	return Signal{State: StateLive, Detail: fmt.Sprintf("command, %s", path)}
}

func (p Prober) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p Prober) httpClient() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

// health is the shape chatgpt-tool serve returns. The pool's profile list
// carries the account emails, so it is read and discarded: nothing here keeps
// it, and no report writes it.
type healthBody struct {
	Status    string `json:"status"`
	Transport string `json:"transport"`
	Pool      struct {
		Verified    int `json:"verified"`
		Concurrency int `json:"concurrency"`
	} `json:"pool"`
}

// Probe checks one route: the host's own health first, then its catalogue, then
// optionally a real completion.
func (p Prober) Probe(ctx context.Context, value Route) Health {
	started := p.now()
	result := Health{Route: value.Name, Model: value.Model, CheckedAt: started}
	finish := func(signal Signal) Health {
		result.State = signal.State
		result.Detail = signal.Detail
		result.ResetsAt = signal.ResetsAt
		result.Latency = p.now().Sub(started)
		return result
	}
	if err := value.Validate(); err != nil {
		return finish(Signal{State: StateBroken, Detail: err.Error()})
	}

	// A command is not something to make a request to. There is no /health to
	// ask, no catalogue to list and no key to be rejected, so the whole of the
	// probe is whether the program is on this machine and, if the caller wants
	// it, whether it will answer. Everything below this line speaks HTTP and
	// would report a working route as broken.
	if value.Command != "" {
		signal := p.command(ctx, value)
		if signal.State != StateLive || !p.Deep {
			return finish(signal)
		}
		return finish(p.ask(ctx, value, &codex.Client{Timeout: p.completionTimeout()}, &result, signal.Detail))
	}

	// A gateway has no /health and no pool, so the catalogue below is the whole
	// probe: it answers only if the endpoint is up and the key is accepted, and
	// it is the same answer that says whether the model is still there.
	var body healthBody
	if !value.Gateway {
		var signal Signal
		body, signal = p.health(ctx, value)
		if signal.State != StateLive {
			return finish(signal)
		}
		result.Transport = body.Transport
		result.Verified = body.Pool.Verified
		result.Declared = body.Pool.Concurrency
		if body.Pool.Verified == 0 {
			// The host is up and has nothing to answer with. Sending it work
			// would produce refusals that read like model failures on every page.
			return finish(Signal{State: StateBroken, Detail: "no verified sessions in the pool, run chatgpt-tool heal-sessions"})
		}
	}

	catalogue, signal := p.catalogue(ctx, value)
	result.Catalogue = catalogue
	if signal.State == StateUnreachable || signal.State == StateUnauthorized {
		return finish(signal)
	}
	// For a gateway this is the only question that was asked, so any answer
	// short of live is the state of the route and there is nothing to fall
	// through to.
	if value.Gateway && signal.State != StateLive {
		return finish(signal)
	}
	// A catalogue that answers and does not name the model is the clearest
	// possible statement that the route file is stale.
	if len(catalogue) > 0 && !slices.Contains(catalogue, value.Model) {
		result.Drift = fmt.Sprintf("model %s is not in the %s catalogue, which lists %d: %s",
			value.Model, value.Name, len(catalogue), strings.Join(catalogue, ", "))
		return finish(Signal{State: StateGone, Detail: result.Drift})
	}

	detail := fmt.Sprintf("%s, %d verified", body.Transport, body.Pool.Verified)
	if value.Gateway {
		result.Transport = "gateway"
		detail = fmt.Sprintf("gateway, %d models", len(catalogue))
	}
	if !p.Deep {
		return finish(Signal{State: StateLive, Detail: detail})
	}

	client, err := value.Client(p.completionTimeout(), 0)
	if err != nil {
		return finish(Signal{State: StateBroken, Detail: err.Error()})
	}
	return finish(p.ask(ctx, value, client, &result, detail))
}

// ask puts a real question, which is the only probe that can tell what a route
// will actually answer with.
func (p Prober) ask(ctx context.Context, value Route, client api.Completer, result *Health, detail string) Signal {
	response, err := client.Complete(ctx, api.Request{
		Model:        value.Model,
		Instructions: "Answer in one word.",
		Input:        probePrompt,
	})
	if err != nil {
		signal := ClassifyError(err)
		// "401 Invalid API key" from a box that was never sent one reads as a
		// rejected credential, and somebody then goes and looks at the box. Say
		// which it is, since we are the ones who know.
		if signal.State == StateUnauthorized && !value.IsCommand() && value.Key() == "" {
			signal.Detail = fmt.Sprintf("no key was sent: set %s, or put it in %s",
				KeyEnv, strings.Join(KeyFiles, " or "))
		}
		return signal
	}
	if strings.TrimSpace(response.Text) == "" {
		return Signal{State: StateBroken, Detail: "completed with empty text"}
	}
	result.Answered = response.Model
	detail = fmt.Sprintf("%s, answered in %s", detail, response.Elapsed.Round(time.Second))
	if result.Downgraded() {
		// The route still works, so it is not broken and not gone: work sent to
		// it will come back, on a lesser model. That is a thing a person decides
		// about, and the only way anybody can decide is if it is said. The board
		// reads the route file and the catalogue, and both of those describe what
		// is on offer rather than what arrived.
		detail = fmt.Sprintf("%s on %s, not %s", detail, response.Model, value.Model)
	}
	return Signal{State: StateLive, Detail: detail}
}

// probePrompt is trivial on purpose. A deep probe of the fleet should cost a
// few hundred tokens, not a page.
const probePrompt = "Reply with the single word ok."

func (p Prober) completionTimeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	// The known baseline is 151 s for nine tokens. Five minutes is slack over
	// that without waiting out a host that has stopped answering entirely.
	return 5 * time.Minute
}

func (p Prober) health(ctx context.Context, value Route) (healthBody, Signal) {
	raw, status, err := p.get(ctx, value, "/health")
	if err != nil {
		return healthBody{}, ClassifyError(err)
	}
	if status < 200 || status >= 300 {
		return healthBody{}, Classify(status, string(raw), nil)
	}
	var body healthBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return healthBody{}, Signal{State: StateBroken, Detail: "health is not valid JSON: " + err.Error()}
	}
	if body.Status != "" && body.Status != "ok" {
		return body, Signal{State: StateBroken, Detail: "health says " + body.Status}
	}
	return body, Signal{State: StateLive, Detail: "ok"}
}

// catalogue calls GET /v1/models. A route whose catalogue call fails is not
// condemned for that alone, because not every compatible server implements it.
func (p Prober) catalogue(ctx context.Context, value Route) ([]string, Signal) {
	raw, status, err := p.get(ctx, value, "/models")
	if err != nil {
		return nil, ClassifyError(err)
	}
	if status < 200 || status >= 300 {
		return nil, Classify(status, string(raw), nil)
	}
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, Signal{State: StateBroken, Detail: "catalogue is not valid JSON: " + err.Error()}
	}
	models := make([]string, 0, len(envelope.Data))
	for _, item := range envelope.Data {
		if item.ID != "" {
			models = append(models, item.ID)
		}
	}
	return models, Signal{State: StateLive, Detail: "ok"}
}

func (p Prober) get(ctx context.Context, value Route, path string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, value.Endpoint(path), nil)
	if err != nil {
		return nil, 0, err
	}
	if key := value.Key(); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	request.Header.Set("User-Agent", UserAgent)
	response, err := p.httpClient().Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	return raw, response.StatusCode, nil
}
