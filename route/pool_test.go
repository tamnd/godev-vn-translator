package route

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a hand-wound clock, because every interesting thing a pool does is
// about waiting and no test should.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// alwaysLive answers every probe with a live host, so these tests are about
// failover rather than about probing, which health_test covers.
type alwaysLive struct{ tick *clock }

func (a alwaysLive) Probe(_ context.Context, value Route) Health {
	return Health{Route: value.Name, State: StateLive, Detail: "ok",
		Model: value.Model, CheckedAt: a.tick.Now()}
}

func testPool(t *testing.T, routes ...Route) (*Pool, *clock) {
	t.Helper()
	tick := newClock()
	pool := NewPool(Registry{Routes: routes})
	pool.Now = tick.Now
	pool.Prober = alwaysLive{tick: tick}
	return pool, tick
}

func live(name string, rank, lanes int) Route {
	return Route{Name: name, Wire: WireChat, BaseURL: "http://127.0.0.1:1/v1",
		Model: "gpt-5", Rank: rank, Concurrency: lanes}
}

func pick(t *testing.T, pool *Pool) (Route, func()) {
	t.Helper()
	value, _, release, err := pool.Pick(context.Background())
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	return value, release
}

func TestPickPrefersRank(t *testing.T) {
	pool, _ := testPool(t, live("server1", 20, 1), live("server3", 10, 1))
	got, release := pick(t, pool)
	defer release()
	if got.Name != "server3" {
		t.Errorf("picked %s, want the top rank", got.Name)
	}
}

// Failover is per call. A chunk that fails on server3 goes to server1 and the
// run keeps moving, which is the whole reason the pool exists.
func TestFailoverToNextRoute(t *testing.T) {
	pool, _ := testPool(t, live("server3", 10, 1), live("server1", 20, 1))
	first, release := pick(t, pool)
	release()
	pool.Fail(first.Name, errors.New("connection refused"))

	second, release := pick(t, pool)
	defer release()
	if second.Name != "server1" {
		t.Errorf("picked %s after server3 failed, want server1", second.Name)
	}
}

// Cooldowns are not uniform because the causes are not. A daily limit that
// names its own reset is honoured to the minute rather than rounded to the
// default half hour.
func TestCooldownsByCause(t *testing.T) {
	// The instant in the last case is absolute, because that is the form a
	// provider actually sends and the only one that means the same thing an
	// hour after the response was written.
	epoch := newClock().Now().Add(4 * time.Minute).Unix()
	for _, c := range []struct {
		name string
		err  error
		want time.Duration
	}{
		{"broken once", errors.New("no ChatGPT response found"), MinCooldown},
		{"rejected key", errors.New("401 unauthorized"), UnauthorizedCooldown},
		{"limit with no instant", errors.New("usage_limit_reached"), QuotaCooldown},
		{"limit that names its reset", fmt.Errorf("429 usage_limit_reached, resets_at: %d", epoch), 4 * time.Minute},
	} {
		pool, tick := testPool(t, live("server3", 10, 1))
		pool.Fail("server3", c.err)

		// Just short of the window it should be cold, and just past it warm.
		tick.advance(c.want - time.Second)
		if _, _, _, err := pool.Pick(context.Background()); err == nil {
			t.Errorf("%s: usable %s early", c.name, time.Second)
		}
		tick.advance(2 * time.Second)
		if _, _, release, err := pool.Pick(context.Background()); err != nil {
			t.Errorf("%s: still cold after %s: %v", c.name, c.want, err)
		} else {
			release()
		}
	}
}

// Repeated ordinary failures double the wait, up to a ceiling. Without the
// ceiling a host that broke six times in a row would be out for the rest of the
// week.
func TestBackoffDoublesToACeiling(t *testing.T) {
	for _, c := range []struct {
		failures int
		want     time.Duration
	}{{1, time.Minute}, {2, 2 * time.Minute}, {3, 4 * time.Minute}, {6, 30 * time.Minute}, {50, MaxCooldown}} {
		if got := cooldownFor(c.failures); got != c.want {
			t.Errorf("cooldownFor(%d) = %s, want %s", c.failures, got, c.want)
		}
	}
}

// A model the host does not serve is not a wait, it is an edit to the route
// file. Retrying it forever would look like a slow fleet.
func TestGoneIsRetired(t *testing.T) {
	pool, tick := testPool(t, live("server3", 10, 1))
	pool.Fail("server3", errors.New("model_not_found"))
	tick.advance(48 * time.Hour)
	_, _, _, err := pool.Pick(context.Background())
	if err == nil {
		t.Fatal("a retired route came back")
	}
	if !strings.Contains(err.Error(), "no routes available") {
		t.Errorf("err = %v, want no routes available", err)
	}
}

// Somebody waiting at a terminal wants to know how long, not just that
// everything is down.
func TestColdErrorNamesTheEarliestReset(t *testing.T) {
	pool, _ := testPool(t, live("server3", 10, 1), live("server1", 20, 1))
	pool.Fail("server3", errors.New("401 unauthorized")) // six hours
	pool.Fail("server1", errors.New("broken"))           // one minute
	_, _, _, err := pool.Pick(context.Background())
	if err == nil {
		t.Fatal("both routes are cold and Pick succeeded")
	}
	if !strings.Contains(err.Error(), "server1 is the first to return") {
		t.Errorf("err = %v, want the nearer reset named", err)
	}
	when, name := pool.EarliestReset()
	if name != "server1" || when.IsZero() {
		t.Errorf("EarliestReset = %s at %s", name, when)
	}
}

// A success clears the count, so a host that fails once an hour is not treated
// as a host that failed twice in a row.
func TestSucceedClearsFailures(t *testing.T) {
	pool, tick := testPool(t, live("server3", 10, 1))
	pool.Fail("server3", errors.New("broken"))
	tick.advance(MinCooldown + time.Second)
	pool.Succeed("server3")
	pool.Fail("server3", errors.New("broken again"))

	tick.advance(MinCooldown + time.Second)
	if _, _, release, err := pool.Pick(context.Background()); err != nil {
		t.Fatalf("second failure cost more than the first: %v", err)
	} else {
		release()
	}
}

// The pool hands out lanes, not permission. server3 takes four calls at once
// and server1 takes one, and a caller with six goroutines must not put six on
// server3.
func TestLanesAreRespected(t *testing.T) {
	pool, _ := testPool(t, live("server3", 10, 2), live("server1", 20, 1))
	if got := pool.Lanes(); got != 3 {
		t.Errorf("Lanes = %d, want 3", got)
	}
	counts := map[string]int{}
	var release []func()
	for range 3 {
		value, done := pick(t, pool)
		counts[value.Name]++
		release = append(release, done)
	}
	if counts["server3"] != 2 || counts["server1"] != 1 {
		t.Errorf("handed out %v, want 2 on server3 and 1 on server1", counts)
	}

	// Every lane is taken. The honest answer is to wait rather than to fail or
	// to pile a fourth call onto a host that said it takes two.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, _, err := pool.Pick(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Pick with every lane full = %v, want to have waited", err)
	}

	release[0]()
	if _, _, done, err := pool.Pick(context.Background()); err != nil {
		t.Errorf("Pick after a lane freed: %v", err)
	} else {
		done()
	}
	for _, done := range release[1:] {
		done()
	}
}

// Releasing twice is easy to write by accident with a defer and an early
// return, and a lane counted back twice is a host quietly running at double the
// concurrency it agreed to.
func TestReleaseIsIdempotent(t *testing.T) {
	pool, _ := testPool(t, live("server3", 10, 1))
	_, release := pick(t, pool)
	release()
	release()

	_, second := pick(t, pool)
	defer second()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, _, err := pool.Pick(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a second lane appeared on a one lane host: %v", err)
	}
}
