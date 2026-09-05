package translate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tamnd/godev-vn-translator/api"
	"github.com/tamnd/godev-vn-translator/content"
	"github.com/tamnd/godev-vn-translator/prompt"
	"github.com/tamnd/godev-vn-translator/quality"
	"github.com/tamnd/godev-vn-translator/queue"
	"github.com/tamnd/godev-vn-translator/route"
)

// fake is a route that answers however the test tells it to.
//
// It is given the piece of English out of the request rather than the whole
// request, because that is what a model is being asked about and a test that
// pattern matched on the instructions would pass while the instructions said
// nothing.
type fake struct {
	answer func(english string, n int) (string, error)

	mu    sync.Mutex
	calls int
	sent  []string
}

func (f *fake) Complete(_ context.Context, request api.Request) (api.Response, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.sent = append(f.sent, request.Instructions+"\n"+request.Input)
	f.mu.Unlock()

	english, ok := prompt.Body(request.Input)
	if !ok {
		return api.Response{}, errors.New("the request has no source in it")
	}
	text, err := f.answer(english, n)
	if err != nil {
		return api.Response{}, err
	}
	return api.Response{Model: request.Model, Text: text}, nil
}

func (f *fake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// live is a prober that says yes without a network.
type live struct{}

func (live) Probe(_ context.Context, value route.Route) route.Health {
	return route.Health{Route: value.Name, State: route.StateLive, Model: value.Model}
}

// harness is a checkout, a queue, a work directory and one route.
type harness struct {
	t      *testing.T
	root   string
	engine *Engine
	fake   *fake
}

func setup(t *testing.T, files map[string]string, answer func(english string, n int) (string, error)) *harness {
	t.Helper()
	root := t.TempDir()
	for rel, text := range files {
		path := filepath.Join(root, content.EnglishDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	q, err := queue.Open(filepath.Join(root, "work", "queue"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{answer: answer}
	registry := route.Registry{Routes: []route.Route{{
		Name: "test", Wire: route.WireChat, BaseURL: "http://127.0.0.1:1",
		Model: "test-model", Concurrency: 1,
	}}}
	pool := route.NewPool(registry)
	pool.Prober = live{}
	pool.Dial = func(route.Route) (api.Completer, error) { return f, nil }

	return &harness{t: t, root: root, fake: f, engine: &Engine{
		Root:  content.Root(root),
		Work:  Store{Root: filepath.Join(root, "work")},
		Queue: q,
		Pool:  pool,
	}}
}

func (h *harness) pairs() []content.Pair {
	h.t.Helper()
	pairs, err := content.Root(h.root).Pairs()
	if err != nil {
		h.t.Fatal(err)
	}
	return pairs
}

// cycle is what a real run does: plan, work the queue, put the files together.
func (h *harness) cycle() (Result, Assembly) {
	h.t.Helper()
	if _, err := h.engine.Plan(h.pairs(), false); err != nil {
		h.t.Fatal(err)
	}
	result, err := h.engine.Run(context.Background(), "", 1)
	if err != nil {
		h.t.Fatal(err)
	}
	assembly, err := h.engine.Assemble(h.pairs())
	if err != nil {
		h.t.Fatal(err)
	}
	return result, assembly
}

func (h *harness) read(rel string) string {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.root, content.VietnameseDir, filepath.FromSlash(rel)))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(raw)
}

const page = `---
title: The unique package
---

The standard library now has a [unique package](https://pkg.go.dev/unique).

## Getting started {#start}

Run it and see.
`

// good is an answer that keeps everything a gate looks at and changes the prose.
func good(english string, _ int) (string, error) {
	out := english
	out = strings.ReplaceAll(out, "The unique package", "Gói unique")
	out = strings.ReplaceAll(out, "The standard library now has a", "Thư viện chuẩn giờ có")
	out = strings.ReplaceAll(out, "unique package](", "gói unique](")
	out = strings.ReplaceAll(out, "Getting started", "Bắt đầu")
	out = strings.ReplaceAll(out, "Run it and see.", "Chạy thử và xem.")
	return out, nil
}

func TestAPageGoesOutComesBackAndIsWritten(t *testing.T) {
	h := setup(t, map[string]string{"blog/unique.md": page}, good)
	result, assembly := h.cycle()

	if result.Done == 0 {
		t.Fatalf("nothing finished: %+v", result)
	}
	if len(assembly.Written) != 1 {
		t.Fatalf("wrote %d files, want 1: %+v", len(assembly.Written), assembly)
	}
	got := h.read("blog/unique.md")
	for _, want := range []string{
		"title: Gói unique",
		"https://pkg.go.dev/unique",
		"## Bắt đầu {#start}",
		"Chạy thử và xem.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the written file does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "The standard library") {
		t.Errorf("the English survived into the written file:\n%s", got)
	}
}

// A page that is nothing but inline SVG has no piece to ask about, and putting
// it back together produces the English byte for byte. Six real pages are this
// shape. Writing one is writing an untranslated file, and the audit is right to
// refuse it, so the file is left alone and the overlay falls back to English.
//
// Before this, the supervisor picked the six up every pass, assembled them,
// took an L11 refusal that no piece could be blamed for, requeued nothing and
// did it again five minutes later.
func TestAPageThatIsAllChartsIsNotWritten(t *testing.T) {
	chart := "<p>\n<svg width=\"70.00em\" height=\"9.20em\" version=\"1.1\">\n" +
		strings.Repeat("<text x=\"0.00em\" y=\"1.20em\"><tspan>Weekly</tspan></text>\n", 40) +
		"</svg>\n"
	h := setup(t, map[string]string{"blog/survey/project.html": chart}, good)
	result, assembly := h.cycle()

	if h.fake.count() != 0 {
		t.Errorf("asked %d questions about a page made of charts, want 0", h.fake.count())
	}
	if result.Done != 0 {
		t.Errorf("finished %d pieces, want 0: %+v", result.Done, result)
	}
	if len(assembly.Written) != 0 || len(assembly.Refused) != 0 {
		t.Errorf("wrote %d and refused %d, want 0 and 0: %+v", len(assembly.Written), len(assembly.Refused), assembly)
	}
	if len(assembly.Copied) != 1 || assembly.Copied[0] != "blog/survey/project.html" {
		t.Errorf("Copied is %v, want the one page", assembly.Copied)
	}
	path := filepath.Join(h.root, content.VietnameseDir, "blog", "survey", "project.html")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a copy of the English was written to %s", path)
	}
}

// The same page with one paragraph of prose in it is an ordinary page. The
// charts are still copied and the paragraph is still asked, so the skip is
// about a file with nothing to ask and not about a file containing an <svg>.
func TestAPageWithChartsAndProseIsWritten(t *testing.T) {
	chart := "<svg width=\"70.00em\" version=\"1.1\">\n" +
		strings.Repeat("<text x=\"0.00em\"><tspan>Weekly</tspan></text>\n", 40) +
		"</svg>\n"
	h := setup(t, map[string]string{"blog/survey/dev.html": chart + "\n<p>Run it and see.\n"}, good)
	_, assembly := h.cycle()

	if len(assembly.Copied) != 0 {
		t.Errorf("Copied is %v, want nothing", assembly.Copied)
	}
	if len(assembly.Written) != 1 {
		t.Fatalf("wrote %d files, want 1: %+v", len(assembly.Written), assembly)
	}
	got := h.read("blog/survey/dev.html")
	if !strings.Contains(got, "Chạy thử và xem.") {
		t.Errorf("the prose was not translated:\n%s", got)
	}
	if !strings.Contains(got, "<tspan>Weekly</tspan>") {
		t.Errorf("the chart did not come through unchanged:\n%s", got)
	}
}

func TestTheManifestSaysWhatItWasMadeFrom(t *testing.T) {
	h := setup(t, map[string]string{"blog/unique.md": page}, good)
	h.cycle()

	manifest, err := quality.LoadManifest(h.root)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := manifest.Get("blog/unique.md")
	if !ok {
		t.Fatalf("no record for the file that was just written: %+v", manifest)
	}
	if record.EnglishSHA256 != content.SHA256(page) {
		t.Error("the record does not name the English it was made from")
	}
	if record.PromptSHA256 == "" {
		t.Error("the record does not name the instructions it was asked for under")
	}
	if record.Route != "test" || record.Model != "test-model" {
		t.Errorf("the record says %s/%s answered", record.Route, record.Model)
	}
	if record.English != 0 {
		t.Errorf("%d pieces were recorded as given up on", record.English)
	}
}

func TestARunTwiceOverTheSameCorpusAsksNothing(t *testing.T) {
	h := setup(t, map[string]string{"blog/unique.md": page}, good)
	h.cycle()
	first := h.fake.count()
	h.cycle()
	if h.fake.count() != first {
		t.Errorf("the second run made %d more calls", h.fake.count()-first)
	}
}

func TestAPieceThatLosesALinkIsAskedAgain(t *testing.T) {
	// The first answer drops the link, which is L07 and a refusal. The repair
	// inside the same attempt puts it back. Nothing should reach the file until
	// it does.
	var dropped bool
	h := setup(t, map[string]string{"blog/unique.md": page},
		func(english string, _ int) (string, error) {
			out, _ := good(english, 0)
			if !dropped && strings.Contains(out, "pkg.go.dev") {
				dropped = true
				return strings.ReplaceAll(out,
					"[gói unique](https://pkg.go.dev/unique)", "gói unique"), nil
			}
			return out, nil
		})
	result, assembly := h.cycle()

	if !dropped {
		t.Fatal("the test never dropped a link, so it proved nothing")
	}
	if result.Repaired != 1 {
		t.Errorf("Repaired is %d, want 1: %+v", result.Repaired, result)
	}
	if len(assembly.Written) != 1 {
		t.Fatalf("the repaired file was not written: %+v", assembly)
	}
	if got := h.read("blog/unique.md"); !strings.Contains(got, "https://pkg.go.dev/unique") {
		t.Errorf("the link is still missing from the written file:\n%s", got)
	}
}

func TestARepairCarriesTheFindingsToTheNextAttempt(t *testing.T) {
	// The repair inside the attempt fails too, so the attempt fails. The next
	// attempt has to go out as a repair and not as a fresh question, because the
	// answer that was wrong is the most useful thing anybody knows about the
	// piece.
	var repaired bool
	h := setup(t, map[string]string{"blog/unique.md": page},
		func(english string, n int) (string, error) {
			out, _ := good(english, 0)
			if !strings.Contains(out, "pkg.go.dev") {
				return out, nil
			}
			if n > 4 {
				return out, nil
			}
			return strings.ReplaceAll(out,
				"[gói unique](https://pkg.go.dev/unique)", "gói unique"), nil
		})
	h.engine.Log = func(string, ...any) {}
	h.cycle()

	for _, sent := range h.fake.sent {
		if strings.Contains(sent, "Give back the whole piece and not a patch.") {
			repaired = true
		}
	}
	if !repaired {
		t.Fatal("no request went out with the repair instructions on it")
	}
	// And the second attempt saw the findings, not a blank slate.
	var carried bool
	for _, sent := range h.fake.sent[1:] {
		if strings.Contains(sent, "L07") && strings.Contains(sent, "links") {
			carried = true
		}
	}
	if !carried {
		t.Error("no request carried the finding that the link was missing")
	}
}

func TestTheLastAttemptKeepsTheEnglishSoTheRestOfThePageShips(t *testing.T) {
	h := setup(t, map[string]string{"blog/unique.md": page},
		func(english string, _ int) (string, error) {
			out, _ := good(english, 0)
			return strings.ReplaceAll(out,
				"[gói unique](https://pkg.go.dev/unique)", "gói unique"), nil
		})
	h.engine.Log = func(string, ...any) {}

	// One run is enough. A failed piece goes back to pending and the worker
	// leases it again, so a single run spends all three attempts on it.
	result, assembly := h.cycle()
	if result.English != 1 {
		t.Fatalf("%d pieces were kept in English, want 1: %+v", result.English, result)
	}
	if len(assembly.Written) != 1 {
		t.Fatalf("the page did not ship: %+v", assembly)
	}
	got := h.read("blog/unique.md")
	if !strings.Contains(got, "https://pkg.go.dev/unique") {
		t.Errorf("the piece that was given up on is not the English:\n%s", got)
	}
	if !strings.Contains(got, "Gói unique") {
		t.Errorf("the front matter, which was fine, was not translated:\n%s", got)
	}
	manifest, err := quality.LoadManifest(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if record, _ := manifest.Get("blog/unique.md"); record.English == 0 {
		t.Error("the manifest does not record that a piece was kept in English")
	}
}

func TestARouteThatDiesDoesNotSpendAnAttempt(t *testing.T) {
	// Twenty one jobs went pending to dead in forty one seconds on this fleet
	// once, three attempts each, without a question leaving the laptop. This is
	// the test for that.
	h := setup(t, map[string]string{"blog/unique.md": page},
		func(string, int) (string, error) { return "", errors.New("tunnel is down") })
	h.engine.Log = func(string, ...any) {}

	if _, err := h.engine.Plan(h.pairs(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Run(context.Background(), "", 1); err == nil {
		t.Fatal("a run with no working route reported success")
	}
	jobs, err := h.engine.Queue.List(queue.StageTranslate, queue.Dead)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Errorf("%d jobs died without a model ever answering", len(jobs))
	}
}

func TestAFenceWrappedRoundTheWholeAnswerIsTakenOff(t *testing.T) {
	h := setup(t, map[string]string{"blog/unique.md": page},
		func(english string, _ int) (string, error) {
			out, _ := good(english, 0)
			return "```markdown\n" + out + "\n```", nil
		})
	if _, assembly := h.cycle(); len(assembly.Written) != 1 {
		t.Fatalf("the wrapped answer was not accepted: %+v", assembly)
	}
	if got := h.read("blog/unique.md"); strings.Contains(got, "```markdown") {
		t.Errorf("the wrapper reached the file:\n%s", got)
	}
}

func TestAPageIsNotWrittenUntilEveryPieceIsIn(t *testing.T) {
	// A long page cut into several pieces where one of them keeps failing must
	// leave nothing on disk, because a half written page is served as if it were
	// whole and the overlay hides the rest in English.
	// This is the Ctrl-C case, which on a run of several hours is not the
	// unusual one. The run stops after three pieces of a seven piece page.
	long := "---\ntitle: Long\n---\n\n" + strings.Repeat(
		"Some prose about the [module reference](/ref/mod).\n\n", 200)
	ctx, stop := context.WithCancel(context.Background())
	h := setup(t, map[string]string{"ref/mod.md": long},
		func(english string, n int) (string, error) {
			if n >= 3 {
				stop()
			}
			return strings.ReplaceAll(english, "Some prose about the", "Đôi lời về"), nil
		})
	h.engine.Log = func(string, ...any) {}
	h.engine.Budget = 2000

	cut, err := h.engine.chunks("ref/mod.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(cut) < 4 {
		t.Fatal("the test page did not come out in several pieces")
	}
	plan, err := h.engine.Plan(h.pairs(), false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Asked != len(cut) {
		t.Fatalf("planned %d asks for %d pieces", plan.Asked, len(cut))
	}
	if _, err := h.engine.Run(ctx, "", 1); err != nil {
		t.Fatal(err)
	}
	assembly, err := h.engine.Assemble(h.pairs())
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(h.root, content.VietnameseDir, "ref", "mod.md")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a half translated page reached _content_vi")
	}
	if len(assembly.Written) != 0 {
		t.Errorf("a page was written while its pieces were still out: %+v", assembly)
	}
	if assembly.Waiting["ref/mod.md"] == 0 {
		t.Error("the page is not reported as waiting on anything")
	}
	// And the pieces that did come back are kept, so the next run does not ask
	// for them again. That is the whole reason the answers are on disk.
	before := h.fake.count()
	h.cycle()
	if asked := h.fake.count() - before; asked >= plan.Asked {
		t.Errorf("the second run asked for %d of %d pieces, so nothing was kept",
			asked, plan.Asked)
	}
	if got := h.read("ref/mod.md"); !strings.Contains(got, "Đôi lời về") {
		t.Errorf("the finished page is not the translation:\n%s", got[:200])
	}
}

func TestTargetRoundTrips(t *testing.T) {
	rel, index, err := ParseTarget(Target("blog/unique.md", 7))
	if err != nil {
		t.Fatal(err)
	}
	if rel != "blog/unique.md" || index != 7 {
		t.Errorf("got %s piece %d", rel, index)
	}
	// The padding is what makes target order the order of the pieces.
	if Target("a.md", 2) >= Target("a.md", 10) {
		t.Error("piece 10 sorts before piece 2")
	}
	// And the group is still the section of the site.
	if got := queue.GroupOf(Target("blog/unique.md", 1)); got != "blog" {
		t.Errorf("the group of a chunk target is %q", got)
	}
}

func TestFitPutsBackTheBlankLinesAtEitherEndOfAPiece(t *testing.T) {
	// Without this the last paragraph of one piece and the first of the next
	// become one paragraph, which no gate reports because the blocks either side
	// of the cut both still exist.
	if got := fit("Đoạn văn.\n", "A paragraph.\n\n"); got != "Đoạn văn.\n\n" {
		t.Errorf("got %q", got)
	}
	// The leading one is the blank line under the front matter, which belongs to
	// the first body piece and which no model writes.
	if got := fit("Hôm nay.\n", "\nToday.\n\n"); got != "\nHôm nay.\n\n" {
		t.Errorf("got %q", got)
	}
	// And a file that does not end in a newline still does not.
	if got := fit("Cuối.\n", "The end."); got != "Cuối." {
		t.Errorf("got %q", got)
	}
}

func TestTheBlankLineUnderTheFrontMatterSurvives(t *testing.T) {
	h := setup(t, map[string]string{"blog/unique.md": page}, good)
	if _, assembly := h.cycle(); len(assembly.Written) != 1 {
		t.Fatalf("nothing was written: %+v", assembly)
	}
	if got := h.read("blog/unique.md"); !strings.Contains(got, "---\n\nThư viện chuẩn") {
		t.Errorf("the body runs straight into the front matter:\n%q", got)
	}
}

func TestAnIndentedFirstLineKeepsItsIndent(t *testing.T) {
	// Four spaces is a code block, so an answer that is right and an answer with
	// its indent trimmed are two different pages.
	if got := clean("    code := true\n"); got != "    code := true" {
		t.Errorf("got %q", got)
	}
	if got := clean("\n\n  hai dòng  \n\n"); got != "  hai dòng" {
		t.Errorf("got %q", got)
	}
}

// TestPlanDry is the flag doing what its help says. `-plan` reads as free, so
// the first thing anyone does with a corpus this size is run it to see how much
// work is there, and for a while that quietly queued the whole site. Undoing it
// meant draining, and draining takes out every pending job and not just the ones
// the plan added.
func TestPlanDry(t *testing.T) {
	h := setup(t, map[string]string{"ref/mod.md": page}, func(english string, _ int) (string, error) {
		return english, nil
	})
	h.engine.Log = func(string, ...any) {}

	dry, err := h.engine.Plan(h.pairs(), true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Asked == 0 {
		t.Fatal("the page planned no asks, so this proves nothing")
	}
	if dry.Added != dry.Asked {
		t.Errorf("a dry plan of an empty queue said %d of %d pieces are new", dry.Added, dry.Asked)
	}
	stats, err := h.engine.Queue.Stats(queue.StageTranslate)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Counts[queue.Pending] != 0 {
		t.Fatalf("a dry plan queued %d jobs", stats.Counts[queue.Pending])
	}

	// And it is a dry run of the real thing, not a different count.
	wet, err := h.engine.Plan(h.pairs(), false)
	if err != nil {
		t.Fatal(err)
	}
	if wet.Added != dry.Added || wet.Asked != dry.Asked || wet.Files != dry.Files {
		t.Errorf("dry plan %+v against the real one %+v", dry, wet)
	}
	if stats, err = h.engine.Queue.Stats(queue.StageTranslate); err != nil {
		t.Fatal(err)
	}
	if stats.Counts[queue.Pending] != wet.Added {
		t.Errorf("the real plan reported %d added and queued %d", wet.Added, stats.Counts[queue.Pending])
	}

	// A dry plan over a queue that is already full reports nothing new, which is
	// the number that says whether starting a run would do anything.
	if dry, err = h.engine.Plan(h.pairs(), true); err != nil {
		t.Fatal(err)
	}
	if dry.Added != 0 {
		t.Errorf("a dry plan over a full queue said %d pieces are new", dry.Added)
	}
}

// TestUnmangleUndoesTheTransport is the damage the browser routes do on the way
// back, written as the pairs that were on disk in work/*/rejected when it was
// found.
func TestUnmangleUndoesTheTransport(t *testing.T) {
	for _, tt := range []struct {
		name    string
		english string
		in      string
		want    string
	}{
		{
			"a link target autolinked inside its own link",
			"See [race condition](https://en.wikipedia.org/wiki/Race_condition).",
			"Xem [điều kiện tranh chấp]([https://en.wikipedia.org/wiki/Race_condition](https://en.wikipedia.org/wiki/Race_condition)).",
			"Xem [điều kiện tranh chấp](https://en.wikipedia.org/wiki/Race_condition).",
		},
		{
			// The shape only means damage when both halves are the same url. A
			// link whose text is a different link is a thing a page can contain
			// and there is no way to know what was meant.
			"two different urls are left alone",
			"See [a](https://a.example) and [b](https://b.example).",
			"Xem [x]([https://a.example](https://b.example)).",
			"Xem [x]([https://a.example](https://b.example)).",
		},
		{
			"backticks escaped in transit",
			"Run `go install` first.",
			"Chạy \\`go install\\` trước.",
			"Chạy `go install` trước.",
		},
		{
			"bold escaped in transit",
			"This is **important**.",
			"Đây là \\*\\*quan trọng\\*\\*.",
			"Đây là **quan trọng**.",
		},
		{
			// The English is the ground truth. blog/declaration-syntax.md is a
			// post about syntax and escapes what it quotes, and an answer that
			// escapes the same thing is right.
			"an escape the English also writes stays",
			`The \* here is literal.`,
			`Dấu \* ở đây là ký tự thường.`,
			`Dấu \* ở đây là ký tự thường.`,
		},
		{
			"a backslash in front of a letter is not an escape",
			"strings.Split(s, \"\\n\")",
			"strings.Split(s, \"\\n\")",
			"strings.Split(s, \"\\n\")",
		},
		{
			// Both defects at once, which is the common case and the reason the
			// backslashes come off first.
			"an escaped self link",
			"See [docs](https://go.dev/doc).",
			"Xem [tài liệu]\\([https://go.dev/doc]\\(https://go.dev/doc\\)\\).",
			"Xem [tài liệu](https://go.dev/doc).",
		},
		{
			// Nine of these reached doc/contribute.html and one reached
			// doc/diagnostics.html, and all ten were dead links on a merged page.
			"a link target autolinked inside an href",
			`<a href="https://go.googlesource.com/go">the repository</a>`,
			`<a href="[https://go.googlesource.com/go](https://go.googlesource.com/go)">kho lưu trữ</a>`,
			`<a href="https://go.googlesource.com/go">kho lưu trữ</a>`,
		},
		{
			"an src attribute too",
			`<img src="/images/gopher.png">`,
			`<img src="[/images/gopher.png](/images/gopher.png)">`,
			`<img src="/images/gopher.png">`,
		},
		{
			// Same rule as the Markdown form. Two different urls is a thing that
			// cannot be repaired without guessing what was meant.
			"two different urls in an href are left alone",
			`<a href="https://a.example">a</a>`,
			`<a href="[https://a.example](https://b.example)">a</a>`,
			`<a href="[https://a.example](https://b.example)">a</a>`,
		},
		{
			// 72 of these were in five stored answers, and 38 of them shipped.
			"a tab that came back as an entity",
			"\tfunc main() {\n\t\tprintln()\n",
			"&#x9;func main() {\n&#x9;&#x9;println()\n",
			"\tfunc main() {\n\t\tprintln()\n",
		},
		{
			// The corpus writes &#39;, &#160;, &#xa0;, &#xb6;, &#x60;, &#x261e;
			// and &#x26; on purpose, so an entity the English writes is the
			// passage asking for it.
			"an entity the English writes as well stays",
			"Don&#39;t use it.",
			"Đừng dùng nó&#39;.",
			"Đừng dùng nó&#39;.",
		},
		{
			// Nothing says the character was wanted raw, so nothing is decided.
			"an entity for a character the English never writes stays",
			"a plain sentence",
			"một câu &#x9; đơn giản",
			"một câu &#x9; đơn giản",
		},
		{
			"a decimal entity works the same way",
			"character U+FFFD '�' starts at byte position 6",
			"ký tự U+FFFD '&#65533;' bắt đầu ở vị trí byte 6",
			"ký tự U+FFFD '�' bắt đầu ở vị trí byte 6",
		},
		{
			// The one that took the site down. A colon escaped inside a double
			// quoted YAML scalar is not an escape YAML defines, so the front
			// matter did not parse, so cmd/golangorg would not start and the
			// export had nothing to crawl. One character, whole site.
			"an escaped colon in front matter",
			`title: "//go:fix inline and the source-level inliner"`,
			`title: "//go\:fix inline và trình nội tuyến cấp mã nguồn"`,
			`title: "//go:fix inline và trình nội tuyến cấp mã nguồn"`,
		},
		{
			"an answer with nothing wrong with it is untouched",
			"See [the docs](/doc/) for **more**.",
			"Xem [tài liệu](/doc/) để biết **thêm**.",
			"Xem [tài liệu](/doc/) để biết **thêm**.",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := unmangle(tt.in, tt.english); got != tt.want {
				t.Errorf("unmangle\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// TestTransportErrorIsNotATranslation is a defect that shipped.
//
// blog/go-slices-usage-and-internals.md is a two line redirect stub, and a run
// left an OpenAI error page underneath it. Everything downstream was working
// correctly: it was a short answer to a short piece, so L03 had nothing to call
// truncation against, and no gate that reads a translation can tell that a
// sentence is a product apologising rather than a document explaining.
func TestTransportErrorIsNotATranslation(t *testing.T) {
	for _, tt := range []struct {
		name    string
		english string
		answer  string
		want    bool
	}{
		{
			"the one that shipped",
			"---\nredirect: /blog/slices-intro\n---\n",
			"Something went wrong. If this issue persists please contact us through our help center at help.openai.com.",
			true,
		},
		{
			"a banner with a translation stuck to it",
			"Go is an open source programming language.",
			"Go là một ngôn ngữ lập trình mã nguồn mở.\n\nHmm...something seems to have gone wrong.",
			true,
		},
		{
			// Three of the survey posts name the products and one reports
			// satisfaction scores for them. Naming a product is not being one.
			"a page that talks about the product is content",
			"The most commonly used AI assistants were ChatGPT (68%) and GitHub Copilot (50%).",
			"Các trợ lý AI được sử dụng phổ biến nhất là ChatGPT (68%) và GitHub Copilot (50%).",
			false,
		},
		{
			// The English is the ground truth here for the same reason it is in
			// unmangle. Both sides saying it makes it the passage.
			"a page that quotes the banner is content",
			"An error page reading Conversation not found is not a 404.",
			"Một trang lỗi ghi Conversation not found không phải là một 404.",
			false,
		},
		{
			"an ordinary translation",
			"Run `go install` first.",
			"Chạy `go install` trước.",
			false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := quality.TransportError(tt.answer, tt.english)
			if (got != "") != tt.want {
				t.Errorf("transportError = %q, want a banner: %v", got, tt.want)
			}
		})
	}
}

// The 32 files the upstream sync moved out from under their translations are
// what a run is for, and this is the shape that stopped every one of them.
//
// L13 says a translation was made from English that has since changed. It is
// true when assembly starts and it is about to stop being true, because the
// record is rewritten from the English on disk the moment the file is written.
// Auditing it there refused the file, the refusal blocked the write, and the
// write was the only thing that would have cleared it.
func TestAStaleRecordDoesNotBlockTheFileThatReplacesIt(t *testing.T) {
	h := setup(t, map[string]string{"blog/unique.md": page}, good)

	// A translation on disk, recorded as made from English that is not the
	// English on disk now.
	vi := filepath.Join(h.root, content.VietnameseDir, "blog", "unique.md")
	if err := os.MkdirAll(filepath.Dir(vi), 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := good(page, 0)
	if err := os.WriteFile(vi, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := quality.LoadManifest(h.root)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Set("blog/unique.md", quality.Record{EnglishSHA256: content.SHA256("an older go.dev")})
	if err := manifest.Write(h.root); err != nil {
		t.Fatal(err)
	}

	_, assembly := h.cycle()
	if len(assembly.Refused) > 0 {
		t.Fatalf("the file was refused: %+v", assembly.Refused)
	}
	if len(assembly.Written) != 1 {
		t.Fatalf("wrote %d files, want 1: %+v", len(assembly.Written), assembly)
	}
	manifest, err = quality.LoadManifest(h.root)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := manifest.Get("blog/unique.md")
	if record.EnglishSHA256 != content.SHA256(page) {
		t.Error("the record still names the English the translation was not made from")
	}
}
