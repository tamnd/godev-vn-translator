package quality

import (
	"strings"
	"testing"

	"github.com/tamnd/godev-vn-translator/content"
	"github.com/tamnd/godev-vn-translator/glossary"
)

// check runs one rule over one pair of strings.
//
// Every test below is a pair of small documents rather than a fixture on disk,
// because the point of a gate is what it does to a document nobody has seen
// yet, and a fixture only ever tests the document it was cut from.
func check(t *testing.T, rule Rule, en, vi string) []Finding {
	t.Helper()
	in := Input{
		Pair:  content.Pair{Rel: "test.md", Kind: content.KindMarkdown, Exists: vi != ""},
		EN:    en,
		VI:    vi,
		ENDoc: content.Parse(en),
		VIDoc: content.Parse(vi),
	}
	return rule.Check(in)
}

func count(t *testing.T, rule Rule, en, vi string, want int) []Finding {
	t.Helper()
	got := check(t, rule, en, vi)
	if len(got) != want {
		t.Errorf("%s: got %d findings, want %d", rule.ID, len(got), want)
		for _, f := range got {
			t.Logf("  %s", f.Msg)
		}
	}
	return got
}

func TestPresence(t *testing.T) {
	in := Input{Pair: content.Pair{Rel: "doc/x.md", Exists: false}}
	if got := rulePresence.Check(in); len(got) != 1 {
		t.Fatalf("a missing translation must be reported once, got %d", len(got))
	}
	in.Pair.Exists = true
	if got := rulePresence.Check(in); len(got) != 0 {
		t.Fatalf("a present translation must be silent, got %d", len(got))
	}
}

func TestUntranslated(t *testing.T) {
	count(t, ruleUntranslated, "# Hello\n", "# Hello\n", 1)
	count(t, ruleUntranslated, "# Hello\n", "# Xin chào\n", 0)
}

func TestTruncation(t *testing.T) {
	// Twenty blocks against six is the shape of contributors-summit-2019.md,
	// where the prose that is there is good and the last sections are gone.
	en := strings.Repeat("paragraph\n\n", 20)
	vi := strings.Repeat("đoạn văn\n\n", 6)
	count(t, ruleTruncation, en, vi, 1)

	// A translator joining two short bullets onto one line is not truncation,
	// and the slack exists so that it does not read as truncation.
	count(t, ruleTruncation, en, strings.Repeat("đoạn văn\n\n", 19), 0)

	// A fenced block counts once however many lines it has, so a code heavy
	// page does not drift under the threshold as the code grows.
	enFence := "intro\n\n```go\n" + strings.Repeat("x := 1\n", 40) + "```\n\noutro\n"
	viFence := "mở đầu\n\n```go\n" + strings.Repeat("x := 1\n", 40) + "```\n\nkết\n"
	count(t, ruleTruncation, enFence, viFence, 0)
}

func TestHeadings(t *testing.T) {
	en := "# One\n\ntext\n\n## Two\n\ntext\n"
	count(t, ruleHeadings, en, "# Một\n\nvăn\n\n## Hai\n\nvăn\n", 0)
	count(t, ruleHeadings, en, "# Một\n\nvăn\n", 1)
	// Same count, wrong depth. A section promoted to a chapter changes the
	// table of contents without changing anything a reader can see.
	count(t, ruleHeadings, en, "# Một\n\nvăn\n\n# Hai\n\nvăn\n", 1)
}

func TestHeadingIDs(t *testing.T) {
	// An explicit id on the English must survive.
	en := "# Overview {#overview}\n\n[jump](#overview)\n"
	count(t, ruleHeadingIDs, en, "# Tổng quan {#overview}\n\n[nhảy](#overview)\n", 0)
	count(t, ruleHeadingIDs, en, "# Tổng quan {#tong-quan}\n\n[nhảy](#tong-quan)\n", 1)

	// The English declares no id, so both sides get one from the renderer and
	// there is nothing to compare. A Vietnamese heading that adds an explicit
	// id is an improvement and must not be reported.
	count(t, ruleHeadingIDs, "# Evaluation\n", "# Đánh giá {#evaluation}\n", 0)

	// An anchor that does not resolve on either side is the English's, not the
	// translation's. This is blog/pgo.md linking to #example in both files.
	dangling := "[a](#example)\n\n# Example\n"
	count(t, ruleHeadingIDs, dangling, "[a](#example)\n\n# Ví dụ\n", 0)

	// An anchor the translation invented is the translation's problem, and it
	// is the one real finding on the corpus.
	count(t, ruleHeadingIDs, dangling, "[a](#vi-du)\n\n# Ví dụ\n", 1)
}

func TestCodeComments(t *testing.T) {
	// Translating the comments inside an example is the whole point of
	// translating a tutorial, and the first version of this rule refused it.
	en := "```go\n// say hello\nfmt.Println(\"hi\")\n```\n"
	vi := "```go\n// nói xin chào\nfmt.Println(\"hi\")\n```\n"
	count(t, ruleCode, en, vi, 0)

	// The code itself is not prose.
	count(t, ruleCode, en, "```go\n// nói xin chào\nfmt.In(\"hi\")\n```\n", 1)

	// A lost block is structure, whatever is in it.
	count(t, ruleCode, en, "", 1)

	// The info string is structure too. A `go` that became `đi` is a block
	// that stops being highlighted and stops being extracted by the playground.
	count(t, ruleCode, en, "```đi\n// nói xin chào\nfmt.Println(\"hi\")\n```\n", 1)

	// A URL is not a comment, however many slashes it has.
	url := "```go\nx := \"https://go.dev\"\n```\n"
	count(t, ruleCode, url, "```go\nx := \"https://godev.vn\"\n```\n", 1)

	// # opens a comment in shell and does not in Go.
	sh := "```sh\n# build it\ngo build ./...\n```\n"
	count(t, ruleCode, sh, "```sh\n# biên dịch\ngo build ./...\n```\n", 0)

	// database.md documents a schema as JSON with // comments on every field,
	// and those comments are the documentation.
	js := "```json\n{\n  // the latest time\n  \"modified\": \"\"\n}\n```\n"
	count(t, ruleCode, js, "```json\n{\n  // thời điểm mới nhất\n  \"modified\": \"\"\n}\n```\n", 0)
}

func TestLinks(t *testing.T) {
	en := "see [the tutorial](/doc/tutorial/json) and [the spec](/ref/spec)\n"
	count(t, ruleLinks, en, "xem [hướng dẫn](/doc/tutorial/json) và [đặc tả](/ref/spec)\n", 0)
	// blog/json.md, where the sentence carrying the link was rewritten into
	// one without it.
	count(t, ruleLinks, en, "xem [đặc tả](/ref/spec)\n", 1)

	// The title after the target is prose. ref/mod.md carries 450 links and the
	// five that differ differ only here.
	title := "![](/doc/mvs/upgrade.svg \"MVS upgrade\")\n"
	count(t, ruleLinks, title, "![](/doc/mvs/upgrade.svg \"Nâng cấp MVS\")\n", 0)

	// Same document anchors belong to L05, which knows what the file declares.
	count(t, ruleLinks, "[a](#top)\n", "[a](#dau-trang)\n", 0)
}

func TestActions(t *testing.T) {
	// site.tmpl's alt text is prose and must be allowed to change.
	en := `{{- $alt := "Go gophers with wrench"}}`
	count(t, ruleActions, en, `{{- $alt := "Go gopher cầm cờ lê"}}`, 0)

	// talks/2016/state-of-go.slide translated the `and` function.
	count(t, ruleActions, "{{- and -}}", "{{- và -}}", 1)

	// The solutions pages carry their whole content in a backquoted argument,
	// so stripping backquotes exempts the prose and keeps the call.
	count(t, ruleActions, "{{projects `- name: Go`}}", "{{projects `- name: Go tiếng Việt`}}", 0)
	count(t, ruleActions, "{{projects `- name: Go`}}", "{{dựán `- name: Go`}}", 1)
}

func TestFrontMatter(t *testing.T) {
	en := "---\ntitle: Gobs of data\ndate: 2011-03-24\n---\n\nbody\n"
	count(t, ruleFrontMatter, en, "---\ntitle: Dữ liệu Gob\ndate: 2011-03-24\n---\n\nthân\n", 0)

	// The 138 file mistake: a template flag the English does not have.
	count(t, ruleFrontMatter, en, "---\ntitle: Dữ liệu Gob\ndate: 2011-03-24\ntemplate: true\n---\n\nthân\n", 1)

	// A date is a date and a name is a name.
	count(t, ruleFrontMatter, en, "---\ntitle: Dữ liệu Gob\ndate: 24-03-2011\n---\n\nthân\n", 1)

	// Right keys, wrong order. solutions/grail.md.
	count(t, ruleFrontMatter, en, "---\ndate: 2011-03-24\ntitle: Dữ liệu Gob\n---\n\nthân\n", 1)
}

func TestTerminology(t *testing.T) {
	g := glossary.Parse("| garbage collector | bộ thu gom rác | |\n| commit | commit | |\n")
	run := func(en, vi string) []Finding {
		return ruleTerminology.Check(Input{
			Pair: content.Pair{Rel: "t.md", Kind: content.KindMarkdown},
			EN:   en, VI: vi,
			ENDoc: content.Parse(en), VIDoc: content.Parse(vi),
			Glossary: g,
		})
	}
	if got := run("the garbage collector runs", "the garbage collector chạy"); len(got) != 1 {
		t.Errorf("a term left in English must be reported, got %d", len(got))
	}
	if got := run("the garbage collector runs", "bộ thu gom rác chạy"); len(got) != 0 {
		t.Errorf("a translated term must be silent, got %d", len(got))
	}
	// The glossary keeps commit in English on purpose.
	if got := run("a commit lands", "một commit được ghi"); len(got) != 0 {
		t.Errorf("a term that keeps its English must be silent, got %d", len(got))
	}
	// A term inside code is not prose.
	if got := run("the garbage collector runs", "chạy `garbage collector`"); len(got) != 0 {
		t.Errorf("a term inside inline code must be silent, got %d", len(got))
	}
}

func TestLanguage(t *testing.T) {
	ascii := strings.Repeat("this is english prose that was never translated. ", 10)
	count(t, ruleLanguage, ascii, ascii, 1)

	viet := strings.Repeat("đây là văn bản tiếng Việt đã được dịch đầy đủ. ", 10)
	count(t, ruleLanguage, ascii, viet, 0)

	// A short page is exempt, because a page that is almost all code
	// legitimately has three words of prose in it.
	count(t, ruleLanguage, "ok", "ok", 0)
}

func TestCommentary(t *testing.T) {
	en := "# Title\n\nbody\n"
	count(t, ruleCommentary, en, "Here is the Vietnamese translation:\n\n# Tiêu đề\n\nthân\n", 1)
	count(t, ruleCommentary, en, "# Tiêu đề\n\nthân\n", 0)

	// A page returned as a listing of itself.
	count(t, ruleCommentary, en, "```markdown\n# Tiêu đề\n\nthân\n```\n", 1)

	// An English page that opens with "Note:" must not make its translation
	// look like commentary.
	note := "Note: this is part of the page.\n"
	count(t, ruleCommentary, note, "Note: đây là một phần của trang.\n", 0)
}

func TestStale(t *testing.T) {
	en := "# Title\n"
	m := &Manifest{}
	in := Input{
		Pair: content.Pair{Rel: "doc/x.md", Kind: content.KindMarkdown, Exists: true},
		EN:   en, VI: "# Tiêu đề\n", Manifest: m,
	}

	// No record at all is a gap in the manifest, not a defect in the file, so
	// it is a notice. The corpus predates the manifest and would otherwise open
	// with 654 refusals.
	got := ruleStale.Check(in)
	if len(got) != 1 || got[0].Severity != Notice {
		t.Fatalf("a file with no record must give one notice, got %#v", got)
	}

	m.Set("doc/x.md", Record{EnglishSHA256: content.SHA256(en)})
	if got := ruleStale.Check(in); len(got) != 0 {
		t.Errorf("a current record must be silent, got %d", len(got))
	}

	in.EN = "# Title, revised\n"
	got = ruleStale.Check(in)
	if len(got) != 1 || got[0].Severity == Notice {
		t.Errorf("English that moved must be refused, got %#v", got)
	}
}

// TestSeverityIsADefault guards the bug that made the first run report 889
// refusals: Audit stamped the rule's severity over the finding's own, so L13's
// notice for a file with no record came out as a refusal.
func TestSeverityIsADefault(t *testing.T) {
	rule := Rule{ID: "TST", Name: "test", Severity: Refuse,
		Check: func(Input) []Finding {
			return []Finding{{Severity: Notice, Msg: "downgraded"}, {Msg: "default"}}
		}}
	var got []Finding
	for _, f := range rule.Check(Input{}) {
		if f.Severity == "" {
			f.Severity = rule.Severity
		}
		got = append(got, f)
	}
	if got[0].Severity != Notice || got[1].Severity != Refuse {
		t.Fatalf("severity is a default, not an override: %#v", got)
	}
}
