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
	if _, err := h.engine.Plan(h.pairs()); err != nil {
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

	if _, err := h.engine.Plan(h.pairs()); err != nil {
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
	plan, err := h.engine.Plan(h.pairs())
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
