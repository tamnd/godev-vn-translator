// Package route names the things a run can send model calls to, orders them,
// and keeps the run moving when one of them stops answering.
//
// A single base URL is fine until the account behind it hits its daily limit,
// which on a ChatGPT web session is a routine event rather than an exceptional
// one. Translating go.dev is 680 files and several thousand chunks, so it will
// happen, and it will happen in the middle of the night. Naming the routes makes
// the failover order something you can read and argue with rather than something
// implicit in a shell script.
//
// There are three kinds and one wire. A command route runs a program on this
// machine, which is the codex subscription. A box route calls chatgpt-tool serve
// over an ssh tunnel, which is server1, server2 and server3. A gateway route
// calls a plain OpenAI compatible endpoint. All three answer the same request
// and return the same response, so nothing above this package knows the
// difference.
package route

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tamnd/godev-vn-translator/api"
)

// Route is one place work can be sent.
type Route struct {
	Name string `json:"name"`
	// Wire is the request format. There is one, because there is one thing on
	// the other end. The field exists so a route file that names something else
	// fails at load with a sentence rather than at the first call with a
	// transport error.
	Wire string `json:"wire"`
	// BaseURL may stop at the server root or end at /v1.
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	// APIKeyEnv names the environment variable holding the key, so a route file
	// can be committed or shared without carrying the secret itself.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// APIKey is a literal key passed on the command line. It never round trips
	// through a route file, because a file meant to be shared would leak it the
	// first time somebody pasted one.
	APIKey string `json:"-"`

	// Host is the ssh destination. It is not used to make the call, which goes
	// over the tunnel to the loopback, but it is what a person needs in order to
	// bring the tunnel up or to look at a log.
	Host string `json:"host,omitempty"`
	// RemotePort and LocalPort describe the tunnel. Something forwards
	// LocalPort here to RemotePort on Host, and BaseURL names LocalPort on the
	// loopback.
	RemotePort int `json:"remote_port,omitempty"`
	LocalPort  int `json:"local_port,omitempty"`

	// Rank orders the routes, lowest first. It is not a quality score, it is the
	// order to try things in.
	Rank int `json:"rank"`
	// Concurrency is how many calls this route will take at once.
	Concurrency int      `json:"concurrency,omitempty"`
	Timeout     Duration `json:"timeout,omitempty"`
	Disabled    bool     `json:"disabled,omitempty"`
	// Note carries why a route is ranked or disabled where it is. A disabled row
	// with no explanation reads as an oversight.
	Note string `json:"note,omitempty"`

	// Gateway says this route is a plain OpenAI compatible endpoint rather than
	// a chatgpt-tool fronting a pool of browser sessions.
	//
	// It matters for one thing: health. A box answers /health with the size of
	// its pool, which is how a route is judged live and how a host with nothing
	// to answer with is caught before a hundred chunks are sent to it. A gateway
	// has no /health to ask. Asked anyway it returns 404, and a working route
	// reads as broken.
	Gateway bool `json:"gateway,omitempty"`

	// Command says this route is neither a box nor an endpoint but a program on
	// the machine the run is on, and names it.
	//
	// The subscription the CLI speaks to is paid for already and needs no
	// browser, no rented box and no key in the environment. It is a third kind
	// because the other two are both limits a run spends its wall clock waiting
	// on: a box runs out of turns and a free gateway answers 429 with fourteen
	// hours on it. A route with a command has no base URL and no wire, since
	// nothing is sent over a socket.
	Command string `json:"command,omitempty"`
}

// WireChat is the only wire: POST /v1/chat/completions, streaming.
const WireChat = "chat"

// Duration is a time.Duration that round trips through JSON as "20m" rather
// than as a nanosecond count nobody can read.
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, err := time.ParseDuration(text)
		if err != nil {
			return fmt.Errorf("parse duration %q: %w", text, err)
		}
		*d = Duration(value)
		return nil
	}
	// A bare number is read as seconds, which is what somebody hand editing a
	// route file most likely meant.
	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err != nil {
		return fmt.Errorf(`duration must be a string like "20m" or a number of seconds`)
	}
	*d = Duration(time.Duration(seconds * float64(time.Second)))
	return nil
}

// Validate reports what is missing rather than letting the route fail at its
// first call with something obscure.
func (r Route) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("route has no name")
	}
	if strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("route %s has no model", r.Name)
	}
	if wire := strings.TrimSpace(r.Wire); wire != "" && wire != WireChat {
		return fmt.Errorf("route %s has unknown wire %q, want %q", r.Name, wire, WireChat)
	}
	// A command is run rather than called, so there is no address to be missing
	// and asking for one would refuse a route that works.
	if strings.TrimSpace(r.BaseURL) == "" && strings.TrimSpace(r.Command) == "" {
		return fmt.Errorf("route %s has no base_url", r.Name)
	}
	if r.Concurrency < 0 {
		return fmt.Errorf("route %s has negative concurrency", r.Name)
	}
	return nil
}

// Lanes is how many calls this route takes at once. A route file that omits it
// gets one, which is slow and correct, rather than unlimited, which is neither.
func (r Route) Lanes() int {
	if r.Concurrency > 0 {
		return r.Concurrency
	}
	return 1
}

// IsCommand says the route is a program to run rather than an address to call.
func (r Route) IsCommand() bool { return strings.TrimSpace(r.Command) != "" }

// Registry is an ordered set of routes.
type Registry struct {
	Routes []Route `json:"routes"`
}

// DefaultPath is where the personal route file lives.
//
// It is outside the repo and it is in .gitignore anyway, because it names hosts
// and it can hold a key.
func DefaultPath() string {
	if value := strings.TrimSpace(os.Getenv("GODEV_ROUTES")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "godev", "routes.json")
}

// Load reads a registry from disk.
func Load(path string) (Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Registry{}, err
	}
	var value Registry
	if err := json.Unmarshal(raw, &value); err != nil {
		return Registry{}, fmt.Errorf("decode route file %s: %w", path, err)
	}
	if err := value.Validate(); err != nil {
		return Registry{}, fmt.Errorf("route file %s: %w", path, err)
	}
	value.sort()
	return value, nil
}

// LoadOrDefault reads the named file, falls back to the personal one, and
// finally to the built-in set. A file named explicitly and missing is an error,
// because ignoring it quietly would run the wrong routes.
func LoadOrDefault(path string) (Registry, string, error) {
	if strings.TrimSpace(path) != "" {
		registry, err := Load(path)
		return registry, path, err
	}
	if personal := DefaultPath(); personal != "" {
		registry, err := Load(personal)
		if err == nil {
			return registry, personal, nil
		}
		if !os.IsNotExist(err) {
			return Registry{}, personal, err
		}
	}
	return Default(), "built-in", nil
}

// Write saves a registry for editing.
func (r Registry) Write(path string) error {
	if directory := filepath.Dir(path); directory != "." {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func (r Registry) Validate() error {
	if len(r.Routes) == 0 {
		return fmt.Errorf("route file lists no routes")
	}
	seen := map[string]bool{}
	ports := map[int]string{}
	for _, value := range r.Routes {
		if err := value.Validate(); err != nil {
			return err
		}
		if seen[value.Name] {
			return fmt.Errorf("route %s is listed twice", value.Name)
		}
		seen[value.Name] = true
		// Two tunnels on one local port is not a typo you find later. The second
		// ssh fails, the first keeps serving, and every call meant for the
		// second host quietly lands on the first.
		if value.LocalPort > 0 {
			if other, ok := ports[value.LocalPort]; ok {
				return fmt.Errorf("routes %s and %s both want local port %d",
					other, value.Name, value.LocalPort)
			}
			ports[value.LocalPort] = value.Name
		}
	}
	return nil
}

func (r *Registry) sort() {
	slices.SortStableFunc(r.Routes, func(a, b Route) int { return a.Rank - b.Rank })
}

// Enabled returns the usable routes in rank order. It sorts rather than
// trusting the caller to have done it, because a registry built in code is easy
// to write out of order and the resulting failover would be silently wrong.
func (r Registry) Enabled() []Route {
	var out []Route
	for _, value := range r.Routes {
		if !value.Disabled {
			out = append(out, value)
		}
	}
	slices.SortStableFunc(out, func(a, b Route) int { return a.Rank - b.Rank })
	return out
}

// Find returns the route with the given name.
func (r Registry) Find(name string) (Route, bool) {
	for _, value := range r.Routes {
		if value.Name == name {
			return value, true
		}
	}
	return Route{}, false
}

// Select restricts the registry to the named routes, in the order given.
//
// The caller's order beats rank, because naming routes explicitly is a
// statement about what to try first, and a named route that the file disables
// is included, since naming it is the override.
func (r Registry) Select(names []string) (Registry, error) {
	var out Registry
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		value, ok := r.Find(name)
		if !ok {
			return Registry{}, fmt.Errorf("unknown route %q, have %s",
				name, strings.Join(r.Names(), ", "))
		}
		value.Disabled = false
		out.Routes = append(out.Routes, value)
	}
	if len(out.Routes) == 0 {
		return Registry{}, fmt.Errorf("no routes selected")
	}
	return out, nil
}

// Names lists every route name in file order.
func (r Registry) Names() []string {
	out := make([]string, 0, len(r.Routes))
	for _, value := range r.Routes {
		out = append(out, value.Name)
	}
	return out
}

// Default is the built-in set, best first.
//
// The local subscription leads. Measured in tamnd/bourbaki-solver on chunks of
// six thousand characters, the full model answers in about forty seconds and a
// box takes forty to seventy, and a box that has quietly stopped takes five
// minutes to say so. It also needs no tunnel, no browser and no key, so it is
// the route that works on a laptop with nothing set up.
//
// Then the three boxes, in the order their capacity says. server3 runs twelve
// verified profiles on 8 cores with 15 GB free, server2 runs ten on 6 cores
// with 7 GB, and server1 has one profile on 4 cores with under a gigabyte,
// which is why it takes the overflow at one lane and not more.
//
// The local ports are the same ones tamnd/bourbaki-solver uses, on purpose. The
// tunnels are already up on a machine that runs that tool, and pointing a
// second set of ports at the same three ssh connections would be two things to
// keep working instead of one.
//
// codex-mini is here and off. The cheap model is roughly twice as quick and it
// is measurably worse at exactly the thing this corpus is: on a chunk the
// gates refused, the cheap model left English terms standing in the Vietnamese
// and the full one did not. The refuse and re-ask loop would catch that and ask
// again, so the cheap model costs a second call on the chunks it gets wrong,
// and translating a documentation site is not the place to trade quality for
// throughput. Turn it on for a bulk re-run where the gates are the safety net.
func Default() Registry {
	registry := Registry{Routes: []Route{
		{
			Name: "codex", Command: CodexCommand, Model: CodexFull,
			Rank: 10, Concurrency: 2, Timeout: Duration(10 * time.Minute),
			Note: "the subscription on this machine, no tunnel and no key",
		},
		{
			Name: "server3", Wire: WireChat, Host: "server3",
			BaseURL: "http://127.0.0.1:18773/v1", Model: DefaultModel,
			APIKeyEnv: KeyEnv, RemotePort: 8077, LocalPort: 18773,
			Rank: 20, Concurrency: 4, Timeout: Duration(20 * time.Minute),
			Note: "12 verified profiles, 8 cores, 15 GB free",
		},
		{
			Name: "server2", Wire: WireChat, Host: "server2",
			BaseURL: "http://127.0.0.1:18772/v1", Model: DefaultModel,
			APIKeyEnv: KeyEnv, RemotePort: 8077, LocalPort: 18772,
			Rank: 30, Concurrency: 3, Timeout: Duration(20 * time.Minute),
			Note: "10 verified profiles, 6 cores, 7 GB free",
		},
		{
			Name: "server1", Wire: WireChat, Host: "server1",
			BaseURL: "http://127.0.0.1:18771/v1", Model: DefaultModel,
			APIKeyEnv: KeyEnv, RemotePort: 8077, LocalPort: 18771,
			Rank: 40, Concurrency: 1, Timeout: Duration(20 * time.Minute),
			Note: "1 profile, 4 cores, under a gigabyte free, so one lane",
		},
		{
			Name: "codex-mini", Command: CodexCommand, Model: CodexMini,
			Rank: 100, Concurrency: 2, Timeout: Duration(10 * time.Minute),
			Disabled: true,
			Note:     "quicker and worse at terminology, for a bulk re-run behind the gates",
		},
	}}
	registry.sort()
	return registry
}

// DefaultModel is the strongest slug the boxes expose.
const DefaultModel = "gpt-5"

// The subscription this machine is signed in to, reached by running the codex
// CLI rather than by calling anything.
const (
	CodexCommand = "codex"
	CodexMini    = "gpt-5.4-mini"
	CodexFull    = "gpt-5.4"
)

// KeyEnv is the variable a box route reads its key from. The key itself is
// never written to a file this repo can see.
const KeyEnv = "GODEV_PROXY_KEY"

// FleetKeyEnv is the variable tamnd/bourbaki-solver uses for the same key to
// the same three servers. It is read as a fallback so that a machine already
// set up for that tool needs nothing done to it for this one.
const FleetKeyEnv = "BOURBAKI_PROXY_KEY"

// KeyFiles are read when neither variable is set in the environment.
//
// This is not a convenience. The fleet's key lives in ~/.config/bourbaki/env as
// a line to be sourced, so a shell that has not sourced it has no key, and the
// three boxes answer every call with 401 Invalid API key. That reads as a
// rejected credential when the truth is that there was never one to reject, and
// it costs an afternoon the first time. Reading the file that already holds the
// key makes the fallback above tell the truth.
var KeyFiles = []string{
	"~/.config/godev/env",
	"~/.config/bourbaki/env",
}

// Key is the credential to send, preferring a literal one from the command line
// over the environment, and the environment over a file on disk.
func (r Route) Key() string {
	if key := strings.TrimSpace(r.APIKey); key != "" {
		return key
	}
	names := []string{r.APIKeyEnv, FleetKeyEnv}
	for _, name := range names {
		if name == "" {
			continue
		}
		if key := strings.TrimSpace(os.Getenv(name)); key != "" {
			return key
		}
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		if key := keyFromFiles(name); key != "" {
			return key
		}
	}
	return ""
}

// assignment matches a shell line that sets one variable, with or without the
// export and with or without quotes. Anything else in the file is passed over,
// because these files are sourced by a shell and may hold whatever else.
var assignment = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$`)

func keyFromFiles(name string) string {
	for _, path := range KeyFiles {
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				continue
			}
			path = filepath.Join(home, path[2:])
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			match := assignment.FindStringSubmatch(line)
			if match == nil || match[1] != name {
				continue
			}
			value := strings.TrimSpace(match[2])
			value = strings.Trim(value, `"'`)
			if value != "" {
				return value
			}
		}
	}
	return ""
}

// Endpoint joins the base URL to a path, tolerating a base that already ends at
// /v1 and one that stops at the server root.
func (r Route) Endpoint(path string) string {
	base := strings.TrimRight(r.BaseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + path
	}
	return base + "/v1" + path
}

// UserAgent identifies this tool in the fleet's logs, so a box serving two
// tools can be told which one is asking.
const UserAgent = "godev-vn-translator"

// Client builds the HTTP transport for a route. A command route has no HTTP
// transport and is built by package codex instead.
func (r Route) Client(timeout time.Duration, maxRetries int) (api.Completer, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.IsCommand() {
		return nil, fmt.Errorf("route %s is a command and has no HTTP client", r.Name)
	}
	if r.Timeout > 0 {
		timeout = r.Timeout.Duration()
	}
	if timeout <= 0 {
		// A long chunk through a browser session takes minutes. Anything
		// resembling an HTTP default would cut every call short.
		timeout = 20 * time.Minute
	}
	return &api.Client{
		URL:           r.Endpoint("/chat/completions"),
		APIKey:        r.Key(),
		HTTPClient:    &http.Client{Timeout: timeout},
		MaxRetries:    maxRetries,
		MaxRetryDelay: MaxRetryDelay,
		UserAgent:     UserAgent,
	}, nil
}

// MaxRetryDelay is the longest wait this tool will sit out because a provider
// asked it to.
//
// The client honours Retry-After, and a provider that names a wait longer than
// this is telling us it is done for now. A free gateway answers a spent
// allowance with 429 and fifteen hours on the header, and a run that takes that
// literally goes to sleep inside one call with nothing in the log to say why.
//
// A minute. Past that the answer is the next route rather than a wait: the
// chunk fails this attempt, goes back on the queue, and the run picks it up
// somewhere that is still answering.
const MaxRetryDelay = time.Minute
