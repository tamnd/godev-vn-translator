package queue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Queue {
	t.Helper()
	q, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func add(t *testing.T, q *Queue, target string) Job {
	t.Helper()
	job := New(StageTranslate, target, "input-"+target, "prompt-v1")
	added, err := q.Add(job)
	if err != nil {
		t.Fatalf("Add %s: %v", target, err)
	}
	if !added {
		t.Fatalf("Add %s: already present", target)
	}
	return job
}

// The id is what makes a rerun cheap: the same work names the same file, so the
// second run of a pipeline adds nothing.
func TestIDIsContentAddressed(t *testing.T) {
	first := NewID(StageTranslate, "ref/mod.md#0045", "abc", "p1")
	if second := NewID(StageTranslate, "ref/mod.md#0045", "abc", "p1"); first != second {
		t.Errorf("the same job got two ids: %s and %s", first, second)
	}
	if len(first) != 16 {
		t.Errorf("id %q is %d characters", first, len(first))
	}
	// A new prompt is new work. If it were not, a prompt change would leave the
	// corpus at the old prompt's output with nothing to show for it.
	if changed := NewID(StageTranslate, "ref/mod.md#0045", "abc", "p2"); changed == first {
		t.Error("changing the prompt did not change the id")
	}
	// The same file translated and then repaired is two jobs, and a queue that
	// gave them one id would drop the second because the first is done.
	if other := NewID(StageRepair, "ref/mod.md#0045", "abc", "p1"); other == first {
		t.Error("two stages share an id")
	}
	// The separator matters: without it, target "ab"+sha "c" and target "a"+sha
	// "bc" would be the same job.
	if a, b := NewID(StageTranslate, "ab", "c", ""), NewID(StageTranslate, "a", "bc", ""); a == b {
		t.Error("the fields run together")
	}
}

func TestAddIsIdempotent(t *testing.T) {
	q := open(t)
	job := add(t, q, "ref/mod.md#0045")

	added, err := q.Add(job)
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("the same job was added twice")
	}

	// A finished job must not come back either, or every rerun of the pipeline
	// would redo the whole corpus.
	leased, err := q.Lease(StageTranslate, "server3", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Finish(leased, true, ""); err != nil {
		t.Fatal(err)
	}
	if added, err := q.Add(job); err != nil || added {
		t.Errorf("a done job was re-added: %t %v", added, err)
	}
}

// A run is started for one section of the site and resolves the English file
// from the section it was started with plus the rest of the target. Hand it a
// job from another section and it reads the wrong file, or reads nothing and
// kills a translation that was fine.
func TestLeaseStaysInsideItsGroup(t *testing.T) {
	q := open(t)
	add(t, q, "blog/unique.md#0066")
	add(t, q, "ref/mod.md#0066")

	job, err := q.Lease(StageTranslate, "server3", "ref", time.Minute)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if job.Target != "ref/mod.md#0066" {
		t.Errorf("leased %s for a run of ref", job.Target)
	}
	if _, err := q.Lease(StageTranslate, "server3", "ref", time.Minute); !errors.Is(err, ErrEmpty) {
		t.Errorf("another section's job was handed to a ref run: %v", err)
	}

	// The other section is untouched and has not spent an attempt, so its own
	// run still finds it waiting.
	other, err := q.Lease(StageTranslate, "server2", "blog", time.Minute)
	if err != nil {
		t.Fatalf("Lease for the other section: %v", err)
	}
	if other.Target != "blog/unique.md#0066" || other.Attempts != 1 {
		t.Errorf("the other section's job came back as %s on attempt %d", other.Target, other.Attempts)
	}
}

// A file is translated front to back. The job ids are content hashes and the
// directory lists them in that order, so the chunks come out shuffled unless
// something sorts them, and a shuffled run finishes no file. A file is only
// worth writing when every chunk of it is back. The chunks here are added in an
// order no sort would produce by accident, including one that hashes before the
// others and one that would come first under a plain alphabetical sort of
// unpadded numbers.
func TestLeaseTakesThePagesInOrder(t *testing.T) {
	q := open(t)
	for _, page := range []string{"0301", "0022", "0107", "0009", "0071"} {
		add(t, q, "doc/install.md#"+page)
	}
	var got []string
	for {
		job, err := q.Lease(StageTranslate, "server3", "doc", time.Minute)
		if errors.Is(err, ErrEmpty) {
			break
		}
		if err != nil {
			t.Fatalf("Lease: %v", err)
		}
		got = append(got, job.Target)
	}
	want := []string{"doc/install.md#0009", "doc/install.md#0022", "doc/install.md#0071", "doc/install.md#0107", "doc/install.md#0301"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("leased\n %v\nwant\n %v", got, want)
	}
}

// The order is per group. A run of one section must not be handed another
// section's chunk just because its number is lower.
func TestLeaseOrdersInsideTheGroupOnly(t *testing.T) {
	q := open(t)
	add(t, q, "blog/unique.md#0001")
	add(t, q, "doc/install.md#0400")
	add(t, q, "doc/install.md#0100")

	for _, want := range []string{"doc/install.md#0100", "doc/install.md#0400"} {
		job, err := q.Lease(StageTranslate, "server3", "doc", time.Minute)
		if err != nil {
			t.Fatalf("Lease: %v", err)
		}
		if job.Target != want {
			t.Errorf("leased %s, want %s", job.Target, want)
		}
	}
}

// A run asked for part of a section takes that part and leaves the rest
// pending. The chunks outside the range are still work and the next run gets
// them. They are just not this run's work, and a queue filled once already
// holds them all. This is how the 41 file sync gap gets translated first
// without draining every section it is spread across.
func TestLeasePartTakesTheRangeAndLeavesTheRest(t *testing.T) {
	q := open(t)
	for _, page := range []string{"0001", "0021", "0022", "0044", "0071", "0072"} {
		add(t, q, "doc/install.md#"+page)
	}
	theGap := func(target string) bool {
		return target >= "doc/install.md#0022" && target <= "doc/install.md#0071"
	}
	var got []string
	for {
		job, err := q.LeasePart(StageTranslate, "server3", "doc", theGap, time.Minute)
		if errors.Is(err, ErrEmpty) {
			break
		}
		if err != nil {
			t.Fatalf("LeasePart: %v", err)
		}
		got = append(got, job.Target)
	}
	want := []string{"doc/install.md#0022", "doc/install.md#0044", "doc/install.md#0071"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("leased\n %v\nwant\n %v", got, want)
	}
	// The three outside the range are untouched rather than failed or dropped.
	stats, err := q.Stats(StageTranslate)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Counts[Pending] != 3 {
		t.Errorf("%d jobs are still pending, want the 3 outside the range", stats.Counts[Pending])
	}
}

func TestLeaseThenFinish(t *testing.T) {
	q := open(t)
	add(t, q, "ref/mod.md#0045")

	job, err := q.Lease(StageTranslate, "server3", "", 10*time.Minute)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if job.Attempts != 1 {
		t.Errorf("attempts = %d after one lease", job.Attempts)
	}
	if job.Lease == nil || job.Lease.Host != "server3" {
		t.Fatalf("lease = %+v", job.Lease)
	}
	// The deadline is what the work is expected to take plus room for running
	// the gates and writing the file, which happen after the model has
	// answered.
	if want := 15 * time.Minute; job.Lease.Until.Sub(job.Created).Round(time.Minute) < want {
		t.Errorf("lease until %s, want at least %s of room", job.Lease.Until, want)
	}

	if _, err := q.Lease(StageTranslate, "server1", "", time.Minute); !errors.Is(err, ErrEmpty) {
		t.Errorf("a leased job was handed out twice: %v", err)
	}

	state, err := q.Finish(job, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if state != Done {
		t.Errorf("state = %s, want done", state)
	}
	if _, _, err := q.Find(StageTranslate, job.ID); err != nil {
		t.Errorf("the finished job is gone: %v", err)
	}
	stats, err := q.Stats(StageTranslate)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Counts[Done] != 1 || stats.Counts[Leased] != 0 {
		t.Errorf("stats = %+v", stats.Counts)
	}
}

// A failure with attempts left goes back to pending, because the next attempt
// lands on a different host and that is usually all it takes.
func TestFailureRetriesThenDies(t *testing.T) {
	q := open(t)
	q.MaxAttempts = 3
	add(t, q, "ref/mod.md#0045")

	for attempt := 1; attempt <= 2; attempt++ {
		job, err := q.Lease(StageTranslate, "server3", "", time.Minute)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		state, err := q.Fail(job, "unbalanced $")
		if err != nil {
			t.Fatal(err)
		}
		if state != Pending {
			t.Fatalf("attempt %d put the job in %s, want pending", attempt, state)
		}
	}

	job, err := q.Lease(StageTranslate, "server2", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if job.Attempts != 3 {
		t.Errorf("attempts = %d on the third lease", job.Attempts)
	}
	state, err := q.Fail(job, "unbalanced $")
	if err != nil {
		t.Fatal(err)
	}
	if state != Dead {
		t.Fatalf("the third failure put the job in %s, want dead", state)
	}

	// A dead job keeps every reason, not just the last one, or the audit has
	// nothing to say beyond that it failed.
	dead, err := q.List(StageTranslate, Dead)
	if err != nil || len(dead) != 1 {
		t.Fatalf("dead = %d %v", len(dead), err)
	}
	if len(dead[0].History) != 3 {
		t.Errorf("history has %d entries, want one per attempt", len(dead[0].History))
	}
	hosts := []string{dead[0].History[0].Host, dead[0].History[2].Host}
	if hosts[0] != "server3" || hosts[1] != "server2" {
		t.Errorf("history lost the hosts: %v", hosts)
	}
	if _, err := q.Lease(StageTranslate, "server3", "", time.Minute); !errors.Is(err, ErrEmpty) {
		t.Errorf("a dead job was handed out again: %v", err)
	}
}

// This is the whole crash recovery story: a worker that is killed leaves a
// lease with a deadline in the past, and any worker starting up reaps it.
func TestExpiredLeasesComeBack(t *testing.T) {
	q := open(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	q.Now = func() time.Time { return now }
	add(t, q, "ref/mod.md#0045")

	job, err := q.Lease(StageTranslate, "server3", "", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Still working. Reaping now would take the job away from a live worker,
	// which is worse than waiting, because both would write the same output.
	reaped, err := q.Reap(StageTranslate)
	if err != nil || len(reaped) != 0 {
		t.Fatalf("reaped a live lease: %v %v", reaped, err)
	}
	stats, _ := q.Stats(StageTranslate)
	if stats.Expired != 0 {
		t.Errorf("expired = %d while the lease is good", stats.Expired)
	}

	now = now.Add(20 * time.Minute)
	stats, _ = q.Stats(StageTranslate)
	if stats.Expired != 1 {
		t.Errorf("expired = %d after the deadline passed", stats.Expired)
	}
	reaped, err = q.Reap(StageTranslate)
	if err != nil || len(reaped) != 1 || reaped[0] != job.ID {
		t.Fatalf("Reap = %v %v", reaped, err)
	}

	back, err := q.Lease(StageTranslate, "server2", "", time.Minute)
	if err != nil {
		t.Fatalf("the reaped job did not come back: %v", err)
	}
	if back.Attempts != 2 {
		t.Errorf("attempts = %d, want the reap to have cost an attempt", back.Attempts)
	}
	if len(back.History) == 0 || !strings.Contains(back.History[0].Reason, "lease expired") {
		t.Errorf("the reap left no reason: %+v", back.History)
	}
}

// A job that has burnt its attempts and is then reaped is dead, not pending
// forever. Without this a host that hangs on every chunk would spin for good.
func TestReapRespectsTheAttemptBound(t *testing.T) {
	q := open(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	q.Now, q.MaxAttempts = func() time.Time { return now }, 1
	add(t, q, "ref/mod.md#0045")

	if _, err := q.Lease(StageTranslate, "server3", "", time.Minute); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err := q.Reap(StageTranslate); err != nil {
		t.Fatal(err)
	}
	stats, _ := q.Stats(StageTranslate)
	if stats.Counts[Dead] != 1 {
		t.Errorf("counts = %+v, want the job dead", stats.Counts)
	}
}

// The claim is the rename. Two workers picking the same job is normal and
// exactly one of them must win, or the same chunk is translated twice and paid
// for twice.
func TestConcurrentLeasesHandEachJobOutOnce(t *testing.T) {
	q := open(t)
	const jobs, workers = 40, 8
	for index := range jobs {
		add(t, q, fmt.Sprintf("ref/mod.md#%04d", index))
	}

	var mu sync.Mutex
	seen := map[string]int{}
	var group sync.WaitGroup
	for worker := range workers {
		group.Go(func() {
			for {
				job, err := q.Lease(StageTranslate, fmt.Sprintf("w%d", worker), "", time.Minute)
				if errors.Is(err, ErrEmpty) {
					return
				}
				if err != nil {
					t.Errorf("Lease: %v", err)
					return
				}
				mu.Lock()
				seen[job.ID]++
				mu.Unlock()
				if _, err := q.Finish(job, true, ""); err != nil {
					t.Errorf("Finish: %v", err)
					return
				}
			}
		})
	}
	group.Wait()

	if len(seen) != jobs {
		t.Errorf("handed out %d distinct jobs, want %d", len(seen), jobs)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("job %s went out %d times", id, count)
		}
	}
	stats, _ := q.Stats(StageTranslate)
	if stats.Counts[Done] != jobs {
		t.Errorf("done = %d, want %d", stats.Counts[Done], jobs)
	}
}

// Adding the same job from several goroutines at once must add it once.
func TestConcurrentAdd(t *testing.T) {
	q := open(t)
	job := New(StageTranslate, "ref/mod.md#0045", "abc", "p1")
	var mu sync.Mutex
	var added int
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			ok, err := q.Add(job)
			if err != nil {
				t.Errorf("Add: %v", err)
				return
			}
			if ok {
				mu.Lock()
				added++
				mu.Unlock()
			}
		})
	}
	group.Wait()
	stats, _ := q.Stats(StageTranslate)
	if stats.Counts[Pending] != 1 {
		t.Errorf("pending = %d after eight adds of one job", stats.Counts[Pending])
	}
	if added != 1 {
		t.Errorf("Add reported %d insertions", added)
	}
}

func TestRetryAndDrain(t *testing.T) {
	q := open(t)
	q.MaxAttempts = 1
	for index := range 3 {
		add(t, q, fmt.Sprintf("ref/mod.md#%04d", index))
	}
	for range 3 {
		job, err := q.Lease(StageTranslate, "server3", "", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.Fail(job, "the tunnel dropped"); err != nil {
			t.Fatal(err)
		}
	}
	stats, _ := q.Stats(StageTranslate)
	if stats.Counts[Dead] != 3 {
		t.Fatalf("counts = %+v", stats.Counts)
	}

	moved, err := q.Retry(StageTranslate)
	if err != nil || moved != 3 {
		t.Fatalf("Retry = %d %v", moved, err)
	}
	stats, _ = q.Stats(StageTranslate)
	if stats.Counts[Pending] != 3 || stats.Counts[Dead] != 0 {
		t.Errorf("counts after retry = %+v", stats.Counts)
	}
	// Retry clears the count, or a job fixed by a person would die again on its
	// first attempt.
	back, err := q.Lease(StageTranslate, "server3", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if back.Attempts != 1 {
		t.Errorf("attempts = %d after retry, want the count cleared", back.Attempts)
	}
	if len(back.History) == 0 {
		t.Error("retry threw away the history, so nobody can see what was wrong")
	}

	drained, err := q.Drain(StageTranslate)
	if err != nil || drained != 2 {
		t.Fatalf("Drain = %d %v", drained, err)
	}
	stats, _ = q.Stats(StageTranslate)
	if stats.Counts[Pending] != 0 {
		t.Errorf("pending = %d after drain", stats.Counts[Pending])
	}
	// Drain leaves the leased job alone: it belongs to a worker that is still
	// running, and it leaves the record of what happened.
	if stats.Counts[Leased] != 1 {
		t.Errorf("drain took a job away from a live worker: %+v", stats.Counts)
	}
}

// A done job asked for again is the same job. That is what -force wants: the
// answer is there and somebody wants another one anyway, usually because the
// account was being served a cut down model when the first one was written,
// which is exactly what server2 was doing on 2026-09-02.
func TestResetAsksForDoneWorkAgain(t *testing.T) {
	q := open(t)
	job := add(t, q, "ref/mod.md#0045")
	leased, err := q.Lease(StageTranslate, "server3", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Finish(leased, true, ""); err != nil {
		t.Fatal(err)
	}
	if added, err := q.Add(job); err != nil || added {
		t.Fatalf("Add of a done job = %v %v, want it left alone", added, err)
	}

	found, err := q.Reset(StageTranslate, job.ID)
	if err != nil || !found {
		t.Fatalf("Reset = %v %v", found, err)
	}
	stats, _ := q.Stats(StageTranslate)
	if stats.Counts[Pending] != 1 || stats.Counts[Done] != 0 {
		t.Fatalf("counts after reset = %+v", stats.Counts)
	}
	back, err := q.Lease(StageTranslate, "server3", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if back.Attempts != 1 {
		t.Errorf("attempts = %d, want the count cleared", back.Attempts)
	}
	if len(back.History) == 0 {
		t.Error("reset threw away the history of the answer that was there")
	}

	// The one it is holding belongs to a worker that has not come back yet, and
	// handing the same work out twice is what the lease is there to prevent.
	if found, err := q.Reset(StageTranslate, back.ID); err != nil || found {
		t.Errorf("Reset of a leased job = %v %v, want it left alone", found, err)
	}
	if found, err := q.Reset(StageTranslate, "no such job"); err != nil || found {
		t.Errorf("Reset of a job that is not there = %v %v", found, err)
	}
}

func TestRetryFromPendingIsRefused(t *testing.T) {
	q := open(t)
	if _, err := q.Retry(StageTranslate, Done); err == nil {
		t.Error("retry from done was accepted")
	}
	if _, err := q.Retry(StageTranslate, Pending); err == nil {
		t.Error("retry from pending was accepted")
	}
}

// Job files are the durable part. Anything the queue writes has to survive a
// process that is killed in the middle of writing it.
func TestWritesAreAtomic(t *testing.T) {
	q := open(t)
	job := add(t, q, "ref/mod.md#0045")
	entries, err := os.ReadDir(q.dir(StageTranslate, Pending))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("a temp file was left behind: %s", entry.Name())
		}
	}
	raw, err := os.ReadFile(filepath.Join(q.dir(StageTranslate, Pending), job.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"target": "ref/mod.md#0045"`) {
		t.Errorf("job file:\n%s", raw)
	}
}

// A queue in a directory that does not exist yet is the first run, not an
// error.
func TestOpenCreatesTheTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work", "queue")
	q, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range Stages {
		for _, state := range States {
			if _, err := os.Stat(q.dir(stage, state)); err != nil {
				t.Errorf("%s/%s: %v", stage, state, err)
			}
		}
	}
	if _, err := q.Lease(StageRepair, "server3", "", time.Minute); !errors.Is(err, ErrEmpty) {
		t.Errorf("an empty stage returned %v, want ErrEmpty", err)
	}
}

// Stages do not see each other, so draining the repair queue cannot take out
// the translation of the sync gap that is halfway through.
func TestStagesAreSeparate(t *testing.T) {
	q := open(t)
	add(t, q, "ref/mod.md#0045")
	if _, err := q.Add(New(StageRepair, "ref/mod.md", "abc", "p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Drain(StageRepair); err != nil {
		t.Fatal(err)
	}
	stats, _ := q.Stats(StageTranslate)
	if stats.Counts[Pending] != 1 {
		t.Errorf("translate = %+v after draining repair", stats.Counts)
	}
	stats, _ = q.Stats(StageRepair)
	if stats.Counts[Pending] != 0 {
		t.Errorf("repair = %+v after draining it", stats.Counts)
	}
}

func TestStatsTotalAndTable(t *testing.T) {
	q := open(t)
	add(t, q, "ref/mod.md#0045")
	add(t, q, "ref/mod.md#0046")
	stats, err := q.Stats(StageTranslate)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total() != 2 {
		t.Errorf("Total = %d", stats.Total())
	}
	table := Table([]Stats{stats})
	if !strings.Contains(table, "translate") || !strings.Contains(table, "pending") {
		t.Errorf("table:\n%s", table)
	}
}

func TestParse(t *testing.T) {
	if stage, err := ParseStage(" Translate "); err != nil || stage != StageTranslate {
		t.Errorf("ParseStage = %q %v", stage, err)
	}
	if _, err := ParseStage("render"); err == nil || !strings.Contains(err.Error(), "repair") {
		t.Errorf("ParseStage(render) = %v, want the valid ones listed", err)
	}
	if state, err := ParseState("Dead"); err != nil || state != Dead {
		t.Errorf("ParseState = %q %v", state, err)
	}
	if _, err := ParseState("stuck"); err == nil {
		t.Error("an unknown state was accepted")
	}
}

func TestAddRejectsAJobWithNoID(t *testing.T) {
	q := open(t)
	if _, err := q.Add(Job{Stage: StageTranslate, Target: "x"}); err == nil {
		t.Error("a job with no id was accepted")
	}
	if _, err := q.Add(Job{ID: "abc", Target: "x"}); err == nil {
		t.Error("a job with no stage was accepted")
	}
}

func TestFindMissing(t *testing.T) {
	q := open(t)
	if _, _, err := q.Find(StageTranslate, "nosuchjob"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Find = %v, want not exist", err)
	}
}

// Meta is how a repair job says which rule it is meant to fix, so it has to
// survive the round trip through the file.
func TestMetaSurvives(t *testing.T) {
	q := open(t)
	job := New(StageTranslate, "ref/mod.md#0045", "abc", "p1")
	job.Meta = map[string]string{"rule": "L07"}
	if _, err := q.Add(job); err != nil {
		t.Fatal(err)
	}
	leased, err := q.Lease(StageTranslate, "server3", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if leased.Meta["rule"] != "L07" {
		t.Errorf("meta = %v", leased.Meta)
	}
	leased.Meta["rule"] = "L03"
	if _, err := q.Fail(leased, "L07: two links dropped"); err != nil {
		t.Fatal(err)
	}
	again, err := q.Lease(StageTranslate, "server3", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if again.Meta["rule"] != "L03" {
		t.Errorf("the updated rule was lost: %v", again.Meta)
	}
}

func TestReleaseGivesTheAttemptBackAndSaysWhy(t *testing.T) {
	q := open(t)
	job := New(StageTranslate, "ref/mod.md#0070", "sha", "prompt")
	if _, err := q.Add(job); err != nil {
		t.Fatal(err)
	}
	leased, err := q.Lease(StageTranslate, "server3", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if leased.Attempts != 1 {
		t.Fatalf("attempts = %d after leasing, want 1", leased.Attempts)
	}

	if err := q.Release(leased, "ssh: no route to host"); err != nil {
		t.Fatal(err)
	}
	back, state, err := q.Find(StageTranslate, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state != Pending {
		t.Errorf("state = %s, want pending", state)
	}
	if back.Attempts != 0 {
		t.Errorf("attempts = %d, want the attempt given back", back.Attempts)
	}
	if back.Lease != nil {
		t.Error("the lease is still on a job that was handed back")
	}
	// Silence would make a job that loops look like a job that never ran.
	if len(back.History) != 1 || !strings.Contains(back.History[0].Reason, "no route to host") {
		t.Errorf("history = %+v, want the reason it came back", back.History)
	}
	if back.History[0].Host != "server3" {
		t.Errorf("history says host %q, want server3", back.History[0].Host)
	}
}

// Three releases in a row must not drive the count below zero, or the job
// becomes one that can never die.
func TestReleasingAJobThatWasNeverLeasedIsHarmless(t *testing.T) {
	q := open(t)
	job := New(StageTranslate, "ref/mod.md#0070", "sha", "prompt")
	if _, err := q.Add(job); err != nil {
		t.Fatal(err)
	}
	if err := q.Release(job, "never mind"); err != nil {
		t.Fatal(err)
	}
	back, _, err := q.Find(StageTranslate, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Attempts != 0 {
		t.Errorf("attempts = %d, want 0", back.Attempts)
	}
}

// Outstanding is what stops one chunk being queued twice. Done is not in it: a
// chunk translated at the old prompt is work again when the prompt changes.
func TestOutstandingIsEveryTargetStillToRun(t *testing.T) {
	q := open(t)
	pending := New(StageTranslate, "ref/mod.md#0061", "a", "p")
	leased := New(StageTranslate, "ref/mod.md#0062", "b", "p")
	dead := New(StageTranslate, "ref/mod.md#0063", "c", "p")
	done := New(StageTranslate, "ref/mod.md#0064", "d", "p")
	for _, job := range []Job{pending, leased, dead, done} {
		if _, err := q.Add(job); err != nil {
			t.Fatal(err)
		}
	}
	// Lease hands out the lowest id first, so they all come out and the ones
	// this test is not using go back. Releasing inside the loop would put a job
	// back where the next Lease finds it, and the loop would never end.
	held := map[string]Job{}
	for range 4 {
		job, err := q.Lease(StageTranslate, "server3", "", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		held[job.ID] = job
	}
	take := func(id string) Job {
		t.Helper()
		job, ok := held[id]
		if !ok {
			t.Fatalf("job %s never came out of the queue", id)
		}
		return job
	}
	if _, err := q.Finish(take(done.ID), true, ""); err != nil {
		t.Fatal(err)
	}
	out := take(dead.ID)
	out.Attempts = DefaultMaxAttempts
	if _, err := q.Fail(out, "no answer came back"); err != nil {
		t.Fatal(err)
	}
	if err := q.Release(take(pending.ID), "putting this one back"); err != nil {
		t.Fatal(err)
	}

	outstanding, err := q.Outstanding(StageTranslate)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]State{
		"ref/mod.md#0061": Pending,
		"ref/mod.md#0062": Leased,
		"ref/mod.md#0063": Dead,
	}
	if len(outstanding) != len(want) {
		t.Fatalf("outstanding = %+v, want %+v", outstanding, want)
	}
	for target, state := range want {
		if outstanding[target] != state {
			t.Errorf("%s = %q, want %q", target, outstanding[target], state)
		}
	}
}

// A job is keyed on its content, so editing the instructions makes a new job for
// the same target and leaves the old one pending beside it. Nothing had to care
// until a run leased by group: two pending jobs, the same target, different ids,
// and two lanes take one each and ask the same question twice.
//
// After one rewrite of the translation prompt in tamnd/bourbaki-solver, 1380
// pending jobs stood for 837 distinct chunks, and the log has the same front
// matter accepted at 12 seconds on one host and again at 1 minute 6 on another.
// The glossary is part of this prompt and it is edited in pull requests against
// the site repo, so it will happen here more often than it happened there.
func TestASupersededJobDoesNotGetAskedBesideTheOneThatReplacedIt(t *testing.T) {
	q := open(t)
	old := New(StageTranslate, "vi-a9dd72/001", "input-1", "prompt-v1")
	fresh := New(StageTranslate, "vi-a9dd72/001", "input-1", "prompt-v2")
	other := New(StageTranslate, "vi-a9dd72/002", "input-2", "prompt-v2")
	for _, job := range []Job{old, fresh, other} {
		if _, err := q.Add(job); err != nil {
			t.Fatal(err)
		}
	}

	dropped, err := q.Supersede(StageTranslate, map[string]string{
		"vi-a9dd72/001": fresh.ID, "vi-a9dd72/002": other.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Fatalf("dropped %d, want the one job the rewrite replaced", dropped)
	}

	// And what is left is one job per chunk, so two lanes cannot both be asking
	// for chunk 1.
	var got []string
	for {
		job, err := q.Lease(StageTranslate, "host", "vi-a9dd72", time.Minute)
		if errors.Is(err, ErrEmpty) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, job.Target)
	}
	if len(got) != 2 || got[0] != "vi-a9dd72/001" || got[1] != "vi-a9dd72/002" {
		t.Fatalf("leased %v, want each chunk once", got)
	}
}

// Supersede is about what is pending. A job a worker is holding is that worker's
// until it finishes or its lease runs out, and done, failed and dead are the
// record of what happened rather than a work list.
func TestSupersedeLeavesEveryOtherStateAlone(t *testing.T) {
	q := open(t)
	held := New(StageTranslate, "vi-a9dd72/001", "input-1", "prompt-v1")
	finished := New(StageTranslate, "vi-a9dd72/002", "input-2", "prompt-v1")
	for _, job := range []Job{held, finished} {
		if _, err := q.Add(job); err != nil {
			t.Fatal(err)
		}
	}
	leased, err := q.Lease(StageTranslate, "host", "vi-a9dd72", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if leased.Target != "vi-a9dd72/001" {
		t.Fatalf("leased %s first", leased.Target)
	}
	second, err := q.Lease(StageTranslate, "host", "vi-a9dd72", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Finish(second, true, ""); err != nil {
		t.Fatal(err)
	}

	// Nothing pending answers to either target, and both are superseded by a
	// version that is not there.
	dropped, err := q.Supersede(StageTranslate, map[string]string{
		"vi-a9dd72/001": "some-other-id", "vi-a9dd72/002": "some-other-id",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		t.Fatalf("dropped %d jobs that were not pending", dropped)
	}
	if _, state, err := q.Find(StageTranslate, held.ID); err != nil || state != Leased {
		t.Errorf("the leased job is %s: %v", state, err)
	}
	if _, state, err := q.Find(StageTranslate, finished.ID); err != nil || state != Done {
		t.Errorf("the finished job is %s: %v", state, err)
	}
}

// A target nobody named is not this run's to judge. A translate run plans one
// section at a time, so every other section's chunks are pending and unmentioned,
// and dropping them would be one run quietly clearing the work list of the next.
func TestSupersedeIgnoresATargetItWasNotAskedAbout(t *testing.T) {
	q := open(t)
	mine := New(StageTranslate, "vi-a9dd72/001", "input-1", "prompt-v1")
	theirs := New(StageTranslate, "vi-563ecb/044", "input-2", "prompt-v1")
	for _, job := range []Job{mine, theirs} {
		if _, err := q.Add(job); err != nil {
			t.Fatal(err)
		}
	}
	dropped, err := q.Supersede(StageTranslate, map[string]string{"vi-a9dd72/001": mine.ID})
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		t.Fatalf("dropped %d, want none", dropped)
	}
	if _, state, err := q.Find(StageTranslate, theirs.ID); err != nil || state != Pending {
		t.Errorf("another section's chunk is %s: %v", state, err)
	}
}

// A chunk the section no longer has goes, and it goes for the same reason a
// superseded one does: nobody is going to answer it. The English was edited, the
// page that had four chunks has two, and chunks three and four are pending with
// nothing behind them. A lane that leases one cannot find a chunk of that number
// to give the answer back to, so it fails, and three failures in a second is the
// whole of what put twenty four dead jobs in the queue this package came from.
func TestSupersedeDropsAChunkTheSectionNoLongerHas(t *testing.T) {
	q := open(t)
	still := New(StageTranslate, "vi-a9dd72/001", "input-1", "prompt-v1")
	gone := New(StageTranslate, "vi-a9dd72/004", "input-4", "prompt-v1")
	elsewhere := New(StageTranslate, "vi-563ecb/004", "input-4", "prompt-v1")
	for _, job := range []Job{still, gone, elsewhere} {
		if _, err := q.Add(job); err != nil {
			t.Fatal(err)
		}
	}
	dropped, err := q.Supersede(StageTranslate, map[string]string{"vi-a9dd72/001": still.ID})
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Fatalf("dropped %d, want the one chunk the section no longer has", dropped)
	}
	if _, state, err := q.Find(StageTranslate, still.ID); err != nil || state != Pending {
		t.Errorf("the chunk that is still there is %s: %v", state, err)
	}
	if _, _, err := q.Find(StageTranslate, gone.ID); !os.IsNotExist(err) {
		t.Errorf("the chunk the section no longer has is still in the queue: %v", err)
	}
	// Chunk four of another section carries the same number and is nobody's to
	// judge here, because keep says nothing about that section at all.
	if _, state, err := q.Find(StageTranslate, elsewhere.ID); err != nil || state != Pending {
		t.Errorf("another section's chunk four is %s: %v", state, err)
	}
}

// A cut down model gets one ask at a chunk and no second one. Its lane leases
// what nobody has answered wrongly yet, the chunk it fails goes back on the pile
// for a lane that can do better, and its own next lease passes over it rather
// than picking its own failure up again.
func TestLeaseWhereLeavesAFailedJobToTheLaneThatCanTakeIt(t *testing.T) {
	q := open(t)
	for _, chunk := range []string{"tour/basics.md#001", "tour/basics.md#002"} {
		add(t, q, chunk)
	}
	firstAsk := func(job Job) bool { return job.Attempts == 0 }

	job, err := q.LeaseWhere(StageTranslate, "codex-mini", "tour", firstAsk, time.Minute)
	if err != nil {
		t.Fatalf("LeaseWhere: %v", err)
	}
	if job.Target != "tour/basics.md#001" {
		t.Fatalf("leased %s, want tour/basics.md#001", job.Target)
	}
	if _, err := q.Fail(job, "two math spans came back wrong"); err != nil {
		t.Fatal(err)
	}

	// The small lane goes on to the chunk nobody has tried.
	next, err := q.LeaseWhere(StageTranslate, "codex-mini", "tour", firstAsk, time.Minute)
	if err != nil {
		t.Fatalf("LeaseWhere: %v", err)
	}
	if next.Target != "tour/basics.md#002" {
		t.Errorf("the small lane took %s, want tour/basics.md#002 and not its own failure", next.Target)
	}
	if _, err := q.LeaseWhere(StageTranslate, "codex-mini", "tour", firstAsk, time.Minute); !errors.Is(err, ErrEmpty) {
		t.Errorf("the small lane found more work: %v", err)
	}

	// The full model takes what the small one got wrong.
	big, err := q.Lease(StageTranslate, "codex", "tour", time.Minute)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if big.Target != "tour/basics.md#001" {
		t.Errorf("the full lane took %s, want the chunk the small one failed", big.Target)
	}
}

// A lease whose worker is gone comes back without waiting out the deadline, and
// one whose worker is alive does not. The pid that stands for a dead worker is
// this test's own process with the lease rewritten to a pid that cannot be
// running, since a pid the test could kill would be racy.
func TestReapTakesBackALeaseWhoseWorkerIsGone(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gone", "here", "elsewhere"} {
		if _, err := q.Add(Job{Stage: "translate", Target: id, ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if _, err := q.Lease("translate", "server3", "", time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	// gone: this machine, a pid that is not running. here: this machine, this
	// process. elsewhere: a pid that is not running but another machine's, which
	// is the case the deadline still has to cover.
	set := func(id string, pid int, worker string) {
		job, err := q.read(Leased, "translate", id)
		if err != nil {
			t.Fatal(err)
		}
		job.Lease.PID, job.Lease.Worker = pid, worker
		if err := q.write(Leased, job); err != nil {
			t.Fatal(err)
		}
	}
	const noSuchPID = 0x7FFFFFF0
	set("gone", noSuchPID, workerName())
	set("here", os.Getpid(), workerName())
	set("elsewhere", noSuchPID, workerName()+"-other")

	reaped, err := q.Reap("translate")
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 || reaped[0] != "gone" {
		t.Fatalf("reaped %v, want [gone]", reaped)
	}
	for _, id := range []string{"here", "elsewhere"} {
		if _, err := q.read(Leased, "translate", id); err != nil {
			t.Errorf("%s should still be leased: %v", id, err)
		}
	}
	if _, err := q.read(Pending, "translate", "gone"); err != nil {
		t.Errorf("gone should be pending again: %v", err)
	}
}
