package chunk

import (
	"strings"
	"testing"

	"github.com/tamnd/godev-vn-translator/content"
)

// join is the property the whole package rests on.
func join(chunks []Chunk) string {
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(c.Text)
	}
	return out.String()
}

func TestTheChunksAreTheFile(t *testing.T) {
	cases := map[string]struct {
		kind content.Kind
		text string
	}{
		"markdown": {content.KindMarkdown, "---\ntitle: A\n---\n\n# One\n\nprose\n\n\n\nmore\n"},
		"no front matter": {content.KindMarkdown,
			"# One\n\nprose\n\nmore\n"},
		"trailing blanks":  {content.KindMarkdown, "a\n\n\n\n"},
		"no final newline": {content.KindMarkdown, "a\n\nb"},
		"crlf":             {content.KindMarkdown, "---\r\ntitle: A\r\n---\r\n\r\nbody\r\n"},
		"empty":            {content.KindMarkdown, ""},
		"html":             {content.KindHTML, "<h2>One</h2>\n\n<p>two</p>\n"},
		"template":         {content.KindTemplate, "{{define \"a\"}}\n\nx\n\n{{end}}\n{{define \"b\"}}\ny\n{{end}}\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// A tiny budget, so the packer is forced to cut everywhere it is
			// allowed to. The join has to hold at every budget and this is the
			// one that exercises the most seams.
			for _, budget := range []int{1, 10, 100, DefaultBudget} {
				got := join(Split("x.md", tc.kind, tc.text, budget))
				if got != tc.text {
					t.Errorf("budget %d: rejoined to %q, want %q", budget, got, tc.text)
				}
			}
		})
	}
}

func TestFrontMatterIsItsOwnPiece(t *testing.T) {
	chunks := Split("blog/x.md", content.KindMarkdown,
		"---\ntitle: Hello\ndate: 2024-01-01\n---\n\nbody\n", 0)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	if chunks[0].Part != PartFrontMatter {
		t.Errorf("first piece is %s, want front matter", chunks[0].Part)
	}
	// The delimiters go with it. They are what makes the rejoin exact, and a
	// front matter chunk without them is a chunk a model would answer without
	// them too.
	if !strings.HasPrefix(chunks[0].Text, "---\n") || !strings.HasSuffix(chunks[0].Text, "---\n") {
		t.Errorf("front matter piece is %q, want the --- lines kept", chunks[0].Text)
	}
	if chunks[1].Part != PartBody || chunks[1].Text != "\nbody\n" {
		t.Errorf("body piece is %s %q", chunks[1].Part, chunks[1].Text)
	}
}

func TestAFileWithNoFrontMatterHasNoFrontMatterPiece(t *testing.T) {
	chunks := Split("x.md", content.KindMarkdown, "# Title\n\nbody\n", 0)
	for _, c := range chunks {
		if c.Part == PartFrontMatter {
			t.Fatalf("invented a front matter piece: %q", c.Text)
		}
	}
}

func TestACutNeverLandsInsideAFence(t *testing.T) {
	// The fence has blank lines in it, which is the only reason a splitter that
	// looks at blank lines would ever cut here.
	text := "one\n\n```go\nfunc main() {\n\n\tprintln(\"hi\")\n\n}\n```\n\ntwo\n"
	for _, budget := range []int{1, 5, 20} {
		for _, c := range Split("x.md", content.KindMarkdown, text, budget) {
			opens := strings.Count(c.Text, "```")
			if opens%2 != 0 {
				t.Fatalf("budget %d: piece %q has an unbalanced fence", budget, c.Text)
			}
		}
	}
}

func TestACutNeverLandsInsideAPre(t *testing.T) {
	// doc/effective_go.html has 148 <pre> blocks and 36 blank lines inside
	// them. This is that file in miniature.
	text := "<p>one</p>\n\n<pre>\nfunc main() {\n\n\tprintln(\"hi\")\n\n}\n</pre>\n\n<p>two</p>\n"
	for _, budget := range []int{1, 5, 20} {
		for _, c := range Split("x.html", content.KindHTML, text, budget) {
			if strings.Count(c.Text, "<pre>") != strings.Count(c.Text, "</pre>") {
				t.Fatalf("budget %d: piece %q cuts a <pre> in half", budget, c.Text)
			}
		}
	}
}

func TestATemplateIsCutOnlyAtADefine(t *testing.T) {
	text := "{{define \"a\"}}\n<p>one</p>\n\n<p>two</p>\n{{end}}\n{{define \"b\"}}\n<p>three</p>\n{{end}}\n"
	chunks := Split("site.tmpl", content.KindTemplate, text, 1)
	if len(chunks) != 2 {
		t.Fatalf("got %d pieces, want one per define", len(chunks))
	}
	for _, c := range chunks {
		if strings.Count(c.Text, "{{define") != strings.Count(c.Text, "{{end}}") {
			t.Errorf("piece %q does not hold a whole define", c.Text)
		}
	}
}

func TestAnInlineChartIsCopiedAndNotAsked(t *testing.T) {
	chart := "<p>\n<!--include uses.svg -->\n<svg width=\"60em\">" +
		strings.Repeat("<path d=\"M0 0 L1 1\"/>", 200) + "</svg>\n</p>\n"
	text := "before\n\n" + chart + "\nafter\n"
	chunks := Split("blog/survey.html", content.KindHTML, text, 0)
	var found bool
	for _, c := range chunks {
		if !c.Verbatim {
			continue
		}
		found = true
		if !strings.Contains(c.Text, "<svg") {
			t.Errorf("piece marked verbatim has no svg in it: %q", c.Text[:40])
		}
	}
	if !found {
		t.Fatal("the chart was going to be sent to a model to copy back")
	}
	// And it comes back out of Assemble without an answer, since nobody is
	// going to be asked for one.
	answers := map[int]string{}
	for _, c := range chunks {
		if !c.Verbatim {
			answers[c.Index] = c.Text
		}
	}
	got, err := Assemble(chunks, answers)
	if err != nil {
		t.Fatal(err)
	}
	if got != text {
		t.Error("a verbatim piece did not come back unchanged")
	}
}

func TestASmallSVGIsStillAsked(t *testing.T) {
	// A decorative icon in a sentence carries a <title> a screen reader says
	// out loud, and that is prose.
	text := "here is <svg><title>a gopher</title></svg> inline\n"
	for _, c := range Split("x.md", content.KindMarkdown, text, 0) {
		if c.Verbatim {
			t.Fatalf("copied a %d byte svg instead of translating its title", len(c.Text))
		}
	}
}

func TestABigBlockIsCutAtLinesAndSaysSo(t *testing.T) {
	block := strings.Repeat("| a row of a very long generated table |\n", 400)
	chunks := Split("x.md", content.KindMarkdown, block, 1000)
	if len(chunks) < 2 {
		t.Fatalf("got %d pieces for a %d byte block", len(chunks), len(block))
	}
	for _, c := range chunks {
		if !c.Split {
			t.Errorf("piece %d came out of a cut block and is not marked", c.Index)
		}
	}
	if join(chunks) != block {
		t.Error("cutting at lines lost bytes")
	}
}

func TestABlockUnderTwiceTheBudgetGoesWhole(t *testing.T) {
	// Between one and two budgets a block is sent whole, because a seam through
	// the middle of a paragraph is worse than a long request.
	block := strings.Repeat("word ", 300) + "\n"
	chunks := Split("x.md", content.KindMarkdown, block, 1000)
	if len(chunks) != 1 {
		t.Fatalf("got %d pieces, want the block whole", len(chunks))
	}
	if chunks[0].Split {
		t.Error("marked as cut when it was not")
	}
}

func TestAPieceIsToldWhereItIs(t *testing.T) {
	text := "# Title\n\n" + strings.Repeat("a paragraph of prose.\n\n", 200) +
		"## Second section\n\n" + strings.Repeat("more prose here.\n\n", 200)
	chunks := Split("x.md", content.KindMarkdown, text, 1000)
	if len(chunks) < 3 {
		t.Fatalf("got %d pieces", len(chunks))
	}
	if chunks[0].Heading != "" {
		t.Errorf("the first piece was told it is under %q", chunks[0].Heading)
	}
	if chunks[1].Heading != "Title" {
		t.Errorf("second piece is under %q, want Title", chunks[1].Heading)
	}
	last := chunks[len(chunks)-1]
	if last.Heading != "Second section" {
		t.Errorf("last piece is under %q, want Second section", last.Heading)
	}
	for i, c := range chunks {
		if c.Index != i+1 || c.Total != len(chunks) {
			t.Errorf("piece %d says it is %d of %d", i+1, c.Index, c.Total)
		}
	}
}

func TestTheHeadingLosesItsAnchor(t *testing.T) {
	text := "## Type parameters {#type-params}\n\n" + strings.Repeat("prose here.\n\n", 100)
	chunks := Split("x.md", content.KindMarkdown, text, 300)
	for _, c := range chunks[1:] {
		if c.Heading != "Type parameters" {
			t.Fatalf("heading is %q, want the anchor left off", c.Heading)
		}
	}
}

func TestACutPrefersASectionBoundary(t *testing.T) {
	// Two sections that together are over budget and each of which fits. The
	// cut should land between them and not in the middle of the first.
	a := "## One\n\n" + strings.Repeat("alpha.\n\n", 60)
	b := "## Two\n\n" + strings.Repeat("beta.\n\n", 60)
	chunks := Split("x.md", content.KindMarkdown, a+b, 1200)
	for _, c := range chunks {
		if strings.Contains(c.Text, "alpha") && strings.Contains(c.Text, "beta") {
			t.Fatal("a piece runs across the section boundary")
		}
	}
}

func TestAssembleRefusesAMissingAnswer(t *testing.T) {
	chunks := Split("x.md", content.KindMarkdown,
		strings.Repeat("a paragraph.\n\n", 400), 500)
	answers := map[int]string{}
	for _, c := range chunks[:len(chunks)-1] {
		answers[c.Index] = c.Text
	}
	// Silently writing the file short is the failure this exists to stop.
	// blog/contributors-summit-2019.md is what that looks like on disk.
	if _, err := Assemble(chunks, answers); err == nil {
		t.Fatal("assembled a file with a piece missing")
	}
}

func TestAnEmptyFileIsStillOnePiece(t *testing.T) {
	chunks := Split("x.md", content.KindMarkdown, "", 0)
	if len(chunks) != 1 || chunks[0].Total != 1 {
		t.Fatalf("got %d pieces for an empty file", len(chunks))
	}
}
