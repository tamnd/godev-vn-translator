package route

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/godev-vn-translator/codex"
)

// The file below is the shape a personal routes.json has, byte for byte. It is
// here so a change to the struct tags fails a test rather than a run.
const routeFile = `{
  "routes": [
    {"name": "codex", "model": "gpt-5.4", "command": "codex", "rank": 10,
     "concurrency": 2, "timeout": "10m"},
    {"name": "server3", "wire": "chat", "base_url": "http://127.0.0.1:18773/v1",
     "model": "gpt-5", "api_key_env": "GODEV_PROXY_KEY", "rank": 20,
     "concurrency": 4, "timeout": "20m", "host": "server3", "local_port": 18773, "remote_port": 8077},
    {"name": "server2", "wire": "chat", "base_url": "http://127.0.0.1:18772/v1",
     "model": "gpt-5", "api_key_env": "GODEV_PROXY_KEY", "rank": 30,
     "concurrency": 3, "timeout": "20m", "disabled": true,
     "note": "serve not running"}
  ]
}
`

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routes.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	registry, err := Load(write(t, routeFile))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := registry.Names(); strings.Join(got, ",") != "codex,server3,server2" {
		t.Errorf("Names = %v, want rank order", got)
	}
	enabled := registry.Enabled()
	if len(enabled) != 2 {
		t.Fatalf("Enabled = %d routes, want 2 with server2 off", len(enabled))
	}
	if !enabled[0].IsCommand() {
		t.Error("the codex route did not come back as a command")
	}
	if enabled[1].Timeout.Duration() != 20*time.Minute {
		t.Errorf("timeout = %s, want 20m", enabled[1].Timeout.Duration())
	}
	if enabled[0].Lanes() != 2 || enabled[1].Lanes() != 4 {
		t.Errorf("lanes = %d, %d; want 2, 4", enabled[0].Lanes(), enabled[1].Lanes())
	}
}

// A route file must never carry the key itself, only the name of the variable
// holding it. Round tripping one through Write is where a literal key would
// leak, so the field has to stay unmarshalled.
func TestKeyNeverRoundTrips(t *testing.T) {
	registry := Registry{Routes: []Route{{
		Name: "server3", Wire: WireChat, BaseURL: "http://127.0.0.1:18773/v1",
		Model: "gpt-5", APIKey: "sk-secret", APIKeyEnv: KeyEnv, Rank: 10,
	}}}
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-secret") {
		t.Fatalf("the key was written to the route file:\n%s", raw)
	}
	if !strings.Contains(string(raw), KeyEnv) {
		t.Errorf("the variable name was dropped:\n%s", raw)
	}
}

func TestKeyPrefersLiteral(t *testing.T) {
	t.Setenv(KeyEnv, "sk-from-env")
	value := Route{APIKeyEnv: KeyEnv}
	if got := value.Key(); got != "sk-from-env" {
		t.Errorf("Key = %q, want the environment", got)
	}
	value.APIKey = "sk-from-flag"
	if got := value.Key(); got != "sk-from-flag" {
		t.Errorf("Key = %q, want the flag to win", got)
	}
}

// The fleet's key lives in a file meant to be sourced by a shell, so a shell
// that has not sourced it has no key and the three boxes answer 401 Invalid API
// key. That reads as a rejected credential rather than a missing one, and a
// deep probe on 2026-09-02 lost an hour to exactly that.
func TestKeyFallsBackToTheFileTheKeyIsAlreadyIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	body := "# a comment\nexport OTHER=nothing\nexport " + FleetKeyEnv + "=sk-from-file\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := KeyFiles
	KeyFiles = []string{path}
	t.Cleanup(func() { KeyFiles = old })

	t.Setenv(KeyEnv, "")
	t.Setenv(FleetKeyEnv, "")
	value := Route{APIKeyEnv: KeyEnv}
	if got := value.Key(); got != "sk-from-file" {
		t.Errorf("Key = %q, want the key out of the file", got)
	}
	// The environment still wins, because a key passed for one run should not
	// be silently replaced by whatever is on disk.
	t.Setenv(KeyEnv, "sk-from-env")
	if got := value.Key(); got != "sk-from-env" {
		t.Errorf("Key = %q, want the environment to win over the file", got)
	}
}

// Two tunnels on one local port is not a typo anyone finds later: the second
// ssh fails, the first keeps serving, and every call meant for the second host
// lands on the first.
func TestDuplicateLocalPortRejected(t *testing.T) {
	body := `{"routes":[
	  {"name":"a","wire":"chat","base_url":"http://127.0.0.1:18773/v1","model":"gpt-5","local_port":18773},
	  {"name":"b","wire":"chat","base_url":"http://127.0.0.1:18773/v1","model":"gpt-5","local_port":18773}]}`
	_, err := Load(write(t, body))
	if err == nil || !strings.Contains(err.Error(), "local port 18773") {
		t.Fatalf("err = %v, want a complaint about the shared port", err)
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"routes":[]}`, "lists no routes"},
		{`{"routes":[{"wire":"chat","base_url":"http://x/v1","model":"m"}]}`, "no name"},
		{`{"routes":[{"name":"a","wire":"chat","base_url":"http://x/v1"}]}`, "no model"},
		{`{"routes":[{"name":"a","wire":"chat","model":"m"}]}`, "no base_url"},
		{`{"routes":[{"name":"a","wire":"grpc","base_url":"http://x/v1","model":"m"}]}`, "unknown wire"},
		{`{"routes":[{"name":"a","wire":"chat","base_url":"http://x/v1","model":"m"},
		             {"name":"a","wire":"chat","base_url":"http://y/v1","model":"m"}]}`, "listed twice"},
	} {
		_, err := Load(write(t, c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Load(%s) = %v, want %q", c.body, err, c.want)
		}
	}
}

// A command route is run rather than called, so asking it for a base URL would
// refuse a route that works perfectly well.
func TestACommandNeedsNoAddress(t *testing.T) {
	registry, err := Load(write(t, `{"routes":[{"name":"codex","model":"gpt-5.4","command":"codex"}]}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := registry.Routes[0].Client(time.Minute, 0); err == nil {
		t.Error("a command route handed back an HTTP client")
	}
}

// A base URL may stop at the root or end at /v1, because both are things people
// paste, and a doubled /v1/v1 is a 404 that reads as a dead host.
func TestEndpoint(t *testing.T) {
	for _, c := range []struct{ base, want string }{
		{"http://127.0.0.1:18773/v1", "http://127.0.0.1:18773/v1/health"},
		{"http://127.0.0.1:18773", "http://127.0.0.1:18773/v1/health"},
		{"http://127.0.0.1:18773/v1/", "http://127.0.0.1:18773/v1/health"},
	} {
		if got := (Route{BaseURL: c.base}).Endpoint("/health"); got != c.want {
			t.Errorf("Endpoint(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

func TestDurationJSON(t *testing.T) {
	var value struct {
		D Duration `json:"d"`
	}
	if err := json.Unmarshal([]byte(`{"d":"20m"}`), &value); err != nil || value.D.Duration() != 20*time.Minute {
		t.Errorf("string form: %s, %v", value.D.Duration(), err)
	}
	// A bare number is what someone hand editing the file is most likely to
	// have meant as seconds.
	if err := json.Unmarshal([]byte(`{"d":90}`), &value); err != nil || value.D.Duration() != 90*time.Second {
		t.Errorf("number form: %s, %v", value.D.Duration(), err)
	}
	raw, err := json.Marshal(value)
	if err != nil || !strings.Contains(string(raw), `"1m30s"`) {
		t.Errorf("Marshal = %s, %v", raw, err)
	}
}

// The built-in registry is the fleet as measured, not an example. Somebody
// running this with no route file gets these five, so the numbers in them are
// the ones that have to be right.
func TestDefaultIsTheMeasuredFleet(t *testing.T) {
	registry := Default()
	if err := registry.Validate(); err != nil {
		t.Fatalf("the built-in registry does not validate: %v", err)
	}
	if got := strings.Join(registry.Names(), ","); got != "codex,server3,server2,server1,codex-mini" {
		t.Fatalf("Names = %s, want rank order", got)
	}

	// The subscription is first because it costs nothing per call and needs no
	// tunnel. The three boxes are ranked by what they have: server3 has 12
	// verified profiles and 8 cores, server1 has one profile and under a
	// gigabyte free, and one lane is all it will take.
	lanes := map[string]int{"codex": 2, "server3": 4, "server2": 3, "server1": 1}
	for name, want := range lanes {
		value, ok := registry.Find(name)
		if !ok {
			t.Fatalf("%s is not in the built-in registry", name)
		}
		if value.Lanes() != want {
			t.Errorf("%s has %d lanes, want %d", name, value.Lanes(), want)
		}
		if strings.TrimSpace(value.Note) == "" {
			t.Errorf("%s has no note saying why it is ranked where it is", name)
		}
	}

	// The mini model is off rather than absent. It is quicker and worse at the
	// terminology, which is a thing to reach for deliberately behind the gates
	// and never a thing to fail over onto.
	mini, ok := registry.Find("codex-mini")
	if !ok || !mini.Disabled {
		t.Error("codex-mini should be present and disabled")
	}
	if len(registry.Enabled()) != 4 {
		t.Errorf("Enabled = %d, want 4", len(registry.Enabled()))
	}

	// Every box points at the loopback, because the call goes through an ssh
	// tunnel. A route file naming the host directly would be a request leaving
	// this machine in the clear.
	for _, value := range registry.Routes {
		if value.IsCommand() {
			continue
		}
		if !strings.Contains(value.BaseURL, "127.0.0.1") {
			t.Errorf("%s calls %s rather than the tunnel", value.Name, value.BaseURL)
		}
		if value.LocalPort == 0 || value.Host == "" {
			t.Errorf("%s does not say which tunnel it needs", value.Name)
		}
	}
}

// Naming a route on the command line is how a disabled one gets used. Refusing
// it because the file says disabled would make the flag useless for the one
// case it exists for.
func TestSelectOverridesDisabled(t *testing.T) {
	registry, err := Default().Select([]string{"codex-mini"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(registry.Enabled()) != 1 || registry.Enabled()[0].Name != "codex-mini" {
		t.Errorf("Enabled = %v", registry.Names())
	}
	if _, err := Default().Select([]string{"server9"}); err == nil {
		t.Error("Select accepted a route that is not in the registry")
	}
}

// The pool is the one place the three kinds of route stop looking alike, and it
// is the only place. A command gets package codex and everything else gets an
// HTTP client, and Pick hands back an api.Completer either way.
func TestPoolBuildsTheRightTransport(t *testing.T) {
	pool := NewPool(Default())
	command, ok := Default().Find("codex")
	if !ok {
		t.Fatal("no codex route")
	}
	client, err := pool.client(command)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, ok := client.(*codex.Client); !ok {
		t.Errorf("a command route got %T, want the CLI", client)
	}

	box, _ := Default().Find("server3")
	client, err = pool.client(box)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, ok := client.(*codex.Client); ok {
		t.Error("a box route got the CLI")
	}
}

// Every route must be willing to walk away from a wait rather than sit inside
// one call while the run does nothing.
func TestNobodySitsOutASuspension(t *testing.T) {
	if MaxRetryDelay > time.Minute {
		t.Errorf("MaxRetryDelay = %s, which is long enough to hide a stalled run", MaxRetryDelay)
	}
}
