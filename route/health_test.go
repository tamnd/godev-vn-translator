package route

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The two bodies below are what server3 and server1 actually returned on
// 2026-08-10, with the profile list cut down to a count. The real list is a
// dozen account addresses, and nothing in this repo has any business carrying
// those, which is also why healthBody does not decode them.
const (
	server3Health = `{"status":"ok","transport":"browser",
	  "codex":{"credentials":1,"ready":1,"error":null},
	  "pool":{"verified":12,"concurrency":4,"profiles":["... 12 verified ..."]}}`
	// server1 answers with no concurrency field at all, which is why Declared
	// may be zero on a live host and the route file's number stands.
	server1Health = `{"status":"ok","transport":"auto",
	  "codex":{"credentials":1,"ready":1,"error":null},
	  "pool":{"verified":1,"profiles":["... 1 verified ..."]}}`
	models = `{"object":"list","data":[{"id":"gpt-5"},{"id":"gpt-5-codex"},
	  {"id":"gpt-5-mini"},{"id":"gpt-4.1"},{"id":"gpt-4.1-mini"}]}`
)

// serve answers /v1/health and /v1/models with the bodies given.
func serve(t *testing.T, health, catalogue string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(health))
		case "/v1/models":
			_, _ = w.Write([]byte(catalogue))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestProbeLive(t *testing.T) {
	server := serve(t, server3Health, models)
	got := Prober{}.Probe(context.Background(), Route{
		Name: "server3", Wire: WireChat, BaseURL: server.URL, Model: "gpt-5"})
	if got.State != StateLive {
		t.Fatalf("State = %s: %s", got.State, got.Detail)
	}
	if got.Transport != "browser" || got.Verified != 12 || got.Declared != 4 {
		t.Errorf("transport=%q verified=%d declared=%d", got.Transport, got.Verified, got.Declared)
	}
	if len(got.Catalogue) != 5 {
		t.Errorf("Catalogue = %v", got.Catalogue)
	}
}

// A gateway has no /health and no pool of sessions, so its catalogue is the
// whole probe. Asking for the health it does not serve returns 404 and the
// route reads as broken while it is answering perfectly well.
func TestProbeAGatewayByItsCatalogueAlone(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"nemotron-3-ultra-free"},{"id":"hy3-free"}]}`))
	}))
	t.Cleanup(server.Close)

	got := Prober{}.Probe(context.Background(), Route{
		Name: "zen", Wire: WireChat, Gateway: true,
		BaseURL: server.URL, Model: "nemotron-3-ultra-free"})
	if got.State != StateLive {
		t.Fatalf("State = %s: %s", got.State, got.Detail)
	}
	if strings.Join(asked, ",") != "/v1/models" {
		t.Errorf("the probe asked for %v, and a gateway serves only its catalogue", asked)
	}
	if got.Transport != "gateway" || got.Detail != "gateway, 2 models" {
		t.Errorf("transport = %q, detail = %q", got.Transport, got.Detail)
	}
}

// The catalogue is the only thing a gateway is asked, so a catalogue that will
// not answer is the state of the route. There is nothing left to fall through
// to, and reporting live off a 500 would send it a file to translate.
func TestAGatewayThatWillNotListItsModelsIsNotLive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`upstream is down`))
	}))
	t.Cleanup(server.Close)

	got := Prober{}.Probe(context.Background(), Route{
		Name: "zen", Wire: WireChat, Gateway: true,
		BaseURL: server.URL, Model: "nemotron-3-ultra-free"})
	if got.State == StateLive {
		t.Fatalf("State = %s: %s", got.State, got.Detail)
	}
}

// A host with no verified sessions is up and has nothing to answer with.
// Sending it work produces refusals on every chunk that read like model
// failures, so it is out until somebody heals the sessions.
func TestProbeNoVerifiedSessions(t *testing.T) {
	body := `{"status":"ok","transport":"browser","pool":{"verified":0,"concurrency":4}}`
	got := Prober{}.Probe(context.Background(), Route{
		Name: "server3", Wire: WireChat, BaseURL: serve(t, body, models).URL, Model: "gpt-5"})
	if got.State != StateBroken || !strings.Contains(got.Detail, "heal-sessions") {
		t.Fatalf("State = %s, detail = %q", got.State, got.Detail)
	}
}

// A catalogue that answers and does not name the model is the clearest possible
// statement that the route file is stale, and no waiting fixes it.
func TestProbeModelDrift(t *testing.T) {
	got := Prober{}.Probe(context.Background(), Route{
		Name: "server3", Wire: WireChat, BaseURL: serve(t, server3Health, models).URL, Model: "gpt-6"})
	if got.State != StateGone {
		t.Fatalf("State = %s: %s", got.State, got.Detail)
	}
	if !strings.Contains(got.Drift, "gpt-6") || !strings.Contains(got.Drift, "gpt-5-mini") {
		t.Errorf("Drift = %q, want the wanted model and what is on offer", got.Drift)
	}
}

// A dead tunnel is a refused connection on a loopback port. It has to read as
// unreachable, because that is the one the operator fixes with fleet up.
func TestProbeDeadTunnel(t *testing.T) {
	got := Prober{}.Probe(context.Background(), Route{
		Name: "server2", Wire: WireChat, BaseURL: "http://127.0.0.1:1/v1", Model: "gpt-5"})
	if got.State != StateUnreachable {
		t.Fatalf("State = %s: %s", got.State, got.Detail)
	}
}

func TestProbeDeep(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(server1Health))
		case "/v1/models":
			_, _ = w.Write([]byte(models))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"c1","choices":[{"message":{"content":"ok"}}]}`))
		}
	}))
	defer server.Close()

	value := Route{Name: "server1", Wire: WireChat, BaseURL: server.URL, Model: "gpt-5"}
	// The shallow probe must not call the model. A model call to this fleet
	// takes minutes, and doctor is meant to answer in under a second.
	if got := (Prober{}).Probe(context.Background(), value); !strings.Contains(got.Detail, "1 verified") {
		t.Errorf("shallow detail = %q", got.Detail)
	}
	if got := (Prober{Deep: true}).Probe(context.Background(), value); !strings.Contains(got.Detail, "answered in") {
		t.Errorf("deep detail = %q, want evidence of a real call", got.Detail)
	}
}

// An account can be moved down between two runs, and neither the route file nor
// the catalogue can see it: both say what is on offer, and only the answer says
// what arrived. A deep probe of the fleet on 2026-09-02 asked server2 for gpt-5
// and got gpt-5-mini back, which is the model that does not know the
// terminology and is a large part of why the gates exist.
func TestProbeDeepSeesADowngrade(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(server1Health))
		case "/v1/models":
			_, _ = w.Write([]byte(models))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"c1","model":"gpt-5-mini","choices":[{"message":{"content":"ok"}}]}`))
		}
	}))
	defer server.Close()

	value := Route{Name: "server1", Wire: WireChat, BaseURL: server.URL, Model: "gpt-5"}
	got := (Prober{Deep: true}).Probe(context.Background(), value)
	// Still live. Work sent here comes back, on a lesser model, and what to do
	// about that is a person's decision rather than the pool's.
	if got.State != StateLive {
		t.Fatalf("State = %s: %s", got.State, got.Detail)
	}
	if !got.Downgraded() || got.Answered != "gpt-5-mini" {
		t.Fatalf("Answered = %q, downgraded = %v", got.Answered, got.Downgraded())
	}
	if !strings.Contains(got.Detail, "gpt-5-mini, not gpt-5") {
		t.Errorf("Detail = %q, want it to say what answered and what was asked for", got.Detail)
	}
	if want := "gpt-5 -> gpt-5-mini"; !strings.Contains(Table([]Health{got}), want) {
		t.Errorf("the board does not say %q", want)
	}

	// And a route that answers on what it was asked for says the one thing.
	fine := got
	fine.Answered = "gpt-5"
	if fine.Downgraded() || strings.Contains(Table([]Health{fine}), "->") {
		t.Error("a route answering on its own model is reported as downgraded")
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   State
	}{
		{"daily limit on a web session", 429, `{"error":{"message":"usage_limit_reached"}}`, StateQuota},
		{"the key is wrong", 401, `{"error":{"message":"invalid api key"}}`, StateUnauthorized},
		{"model was renamed", 400, `{"error":{"message":"model_not_found"}}`, StateGone},
		{"the browser found nothing on the page", 200, "no ChatGPT response found", StateBroken},
		{"a 200 with nothing in it", 200, "", StateBroken},
		{"gateway fell over", 502, "<html>502 Bad Gateway</html>", StateBroken},
		{"ordinary answer", 200, "hello", StateLive},
		// The codex CLI writes its limit as a sentence for a person rather than
		// as a code. Filed as broken it earns a short cooldown and a retry
		// every few minutes for the five days it has left to run.
		{"the subscription is spent", 0,
			"You've hit your usage limit. Upgrade to Pro or try again at Sep 7th, 2026 9:52 AM.", StateQuota},
	} {
		if got := Classify(c.status, c.body, nil); got.State != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, got.State, got.Detail, c.want)
		}
	}
}

// The reset instant is the difference between cooling a host for half an hour
// and cooling it for the four minutes the provider actually asked for.
func TestResetsFrom(t *testing.T) {
	got := resetsFrom(`{"error":{"message":"usage_limit_reached","resets_at":1786000000}}`)
	if want := time.Unix(1786000000, 0).UTC(); !got.Equal(want) {
		t.Errorf("resets_at = %s, want %s", got, want)
	}
	got = resetsFrom(`rate limited, retry_after: 240`)
	if delay := time.Until(got).Round(time.Second); delay != 240*time.Second {
		t.Errorf("retry_after gave %s, want 4m", delay)
	}
	if got := resetsFrom("nothing here"); !got.IsZero() {
		t.Errorf("a body with no instant gave %s, want the zero time", got)
	}
	// The CLI names the hour in words and in no zone, so it means the hour on
	// the machine it is running on.
	got = resetsFrom("or try again at Sep 7th, 2026 9:52 AM.")
	want := time.Date(2026, 9, 7, 9, 52, 0, 0, time.Local).UTC()
	if !got.Equal(want) {
		t.Errorf("try again at = %s, want %s", got, want)
	}
}
