package prompt

import (
	"strings"
	"testing"

	"github.com/tamnd/godev-vn-translator/chunk"
	"github.com/tamnd/godev-vn-translator/content"
)

func body(t *testing.T, a Ask) string {
	t.Helper()
	text, err := a.Text()
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func markdown(text string) chunk.Chunk {
	return chunk.Chunk{
		Rel: "blog/unique.md", Kind: content.KindMarkdown, Part: chunk.PartBody,
		Index: 1, Total: 1, Text: text,
	}
}

func TestTheSourceGoesInAndComesBackOut(t *testing.T) {
	source := "The standard library of Go 1.23 now includes the\n[new `unique` package](https://pkg.go.dev/unique).\n"
	ask := body(t, Ask{Chunk: markdown(source)})
	got, ok := Body(ask)
	if !ok {
		t.Fatal("could not find the source in the request")
	}
	if got != strings.TrimSpace(source) {
		t.Errorf("got %q, want %q", got, strings.TrimSpace(source))
	}
}

func TestTheSourceIsNotTrimmedOnTheLeft(t *testing.T) {
	// Four spaces is a code block in Markdown, so a chunk that starts indented
	// has to arrive indented.
	source := "    indented := true\n"
	ask := body(t, Ask{Chunk: markdown(source)})
	if !strings.Contains(ask, "\n    indented := true\n") {
		t.Error("the leading indent was trimmed off the source")
	}
}

func TestAPieceIsToldWhereItIs(t *testing.T) {
	c := markdown("some prose\n")
	c.Index, c.Total, c.Heading = 3, 7, "Getting started"
	ask := body(t, Ask{Chunk: c})
	for _, want := range []string{
		"blog/unique.md", "piece 3 of 7", "do not write an ending",
		`"Getting started"`,
	} {
		if !strings.Contains(ask, want) {
			t.Errorf("the request does not say %q", want)
		}
	}
}

func TestTheFirstPieceIsNotToldAboutAnOpening(t *testing.T) {
	c := markdown("some prose\n")
	c.Index, c.Total = 1, 4
	ask := body(t, Ask{Chunk: c})
	if strings.Contains(ask, "do not write an opening") {
		t.Error("the first piece was told not to write an opening")
	}
	if !strings.Contains(ask, "do not write an ending") {
		t.Error("the first piece was not told that more of the page follows")
	}
}

func TestTheLastPieceIsToldItIsTheLast(t *testing.T) {
	c := markdown("some prose\n")
	c.Index, c.Total = 4, 4
	ask := body(t, Ask{Chunk: c})
	if !strings.Contains(ask, "ends where the page ends") {
		t.Error("the last piece was not told it is the last")
	}
}

func TestAWholePageIsToldNothingAboutPieces(t *testing.T) {
	ask := body(t, Ask{Chunk: markdown("some prose\n")})
	if strings.Contains(ask, "piece 1 of 1") {
		t.Error("a page that fits in one request was told it is a piece")
	}
}

func TestACutBlockSaysSo(t *testing.T) {
	c := markdown("of a very long table |\n")
	c.Index, c.Total, c.Split = 2, 3, true
	ask := body(t, Ask{Chunk: c})
	if !strings.Contains(ask, "middle of a block") {
		t.Error("a piece cut out of the middle of a block was not told")
	}
}

func TestEachKindIsDescribed(t *testing.T) {
	want := map[content.Kind]string{
		content.KindHTML:     "tags and the attributes are structure",
		content.KindSlide:    "beginning with a star is a section",
		content.KindTemplate: "between two braces is copied exactly",
		content.KindYAML:     "keys are structure and are never translated",
	}
	for kind, phrase := range want {
		c := markdown("x\n")
		c.Kind = kind
		if ask := body(t, Ask{Chunk: c}); !strings.Contains(ask, phrase) {
			t.Errorf("%s is not described: want %q", kind, phrase)
		}
	}
}

func TestAPieceWithNoGlossaryTermsSaysSo(t *testing.T) {
	// A heading with an empty list under it reads as a list that failed to
	// arrive, and most chunks in this corpus contain no glossary term at all.
	ask := body(t, Ask{Chunk: markdown("x\n")})
	if !strings.Contains(ask, "No term in the glossary appears in this piece.") {
		t.Error("an empty glossary was sent as an empty heading")
	}
}

func TestTheGlossaryGoesInWhenThereIsOne(t *testing.T) {
	ask := body(t, Ask{Chunk: markdown("x\n"), Glossary: "garbage collector  ->  bộ gom rác"})
	if !strings.Contains(ask, "garbage collector  ->  bộ gom rác") {
		t.Error("the glossary did not reach the request")
	}
}

func TestFrontMatterGetsItsOwnInstructions(t *testing.T) {
	c := markdown("---\ntitle: New unique package\n---\n")
	c.Part = chunk.PartFrontMatter
	ask := body(t, Ask{Chunk: c})
	if !strings.Contains(ask, "Never add a key") {
		t.Error("the front matter was asked for with the body instructions")
	}
	// The verbatim key list is generated from content.VerbatimKeys, so the
	// instruction and the gate cannot drift apart.
	for _, key := range content.VerbatimKeys {
		if !strings.Contains(ask, "\n  "+key+"\n") {
			t.Errorf("the front matter request does not list %q as copied", key)
		}
	}
	if !strings.Contains(ask, "Do not add it.") {
		t.Error("the front matter request does not warn about template: true")
	}
}

func TestABodyIsNotAskedWithTheFrontMatterInstructions(t *testing.T) {
	ask := body(t, Ask{Chunk: markdown("prose\n")})
	if strings.Contains(ask, "Never add a key") {
		t.Error("a body piece got the front matter instructions")
	}
}

func TestARepairCarriesWhatWasWrongWithIt(t *testing.T) {
	a := Ask{
		Chunk:    markdown("The [unique package](https://pkg.go.dev/unique) is new.\n"),
		Previous: "Gói unique là mới.\n",
		Findings: []string{
			"L07 blog/unique.md:1 links: has 0 links and the English has 1",
		},
	}
	if !a.Repair() {
		t.Fatal("an ask with a previous answer and a finding is not a repair")
	}
	ask := body(t, a)
	for _, want := range []string{
		"Gói unique là mới.",
		"L07 blog/unique.md:1 links",
		"Give back the whole piece and not a patch.",
	} {
		if !strings.Contains(ask, want) {
			t.Errorf("the repair request does not carry %q", want)
		}
	}
	// The English is still the first thing between a pair of fences, so a
	// reader of a failed job finds the source where they find it everywhere.
	if got, ok := Body(ask); !ok || !strings.Contains(got, "pkg.go.dev/unique") {
		t.Errorf("the English is not where Body looks for it: %q", got)
	}
}

func TestAPreviousAnswerWithNoFindingsIsNotARepair(t *testing.T) {
	// Otherwise a caller that kept the last answer around for its own reasons
	// gets the repair instructions and is asked to fix nothing.
	a := Ask{Chunk: markdown("x\n"), Previous: "y\n"}
	if a.Repair() {
		t.Error("an ask with no findings was treated as a repair")
	}
}

func TestACopiedPieceIsNeverAsked(t *testing.T) {
	c := markdown("<svg>...</svg>\n")
	c.Verbatim = true
	if _, err := (Ask{Chunk: c}).Text(); err == nil {
		t.Fatal("built a request for a piece that is copied through")
	}
}

func TestTheNoteIsOptional(t *testing.T) {
	plain := body(t, Ask{Chunk: markdown("x\n")})
	withNote := body(t, Ask{Chunk: markdown("x\n"), Note: "This page is a redirect stub."})
	if strings.Contains(plain, "redirect stub") {
		t.Error("a note appeared in a request that has none")
	}
	if !strings.Contains(withNote, "This page is a redirect stub.") {
		t.Error("the note did not reach the request")
	}
}

func TestNoPlaceholderSurvives(t *testing.T) {
	// A placeholder left in the text is an instruction the model reads as
	// literal, and it is the kind of thing that is obvious in a diff and
	// invisible in a request that is four thousand characters long.
	asks := []Ask{
		{Chunk: markdown("x\n")},
		{Chunk: func() chunk.Chunk {
			c := markdown("---\ntitle: A\n---\n")
			c.Part = chunk.PartFrontMatter
			return c
		}()},
		{Chunk: markdown("x\n"), Previous: "y\n", Findings: []string{"L04 headings"}},
	}
	for i, a := range asks {
		text := body(t, a)
		for _, name := range []string{"WHERE", "RULES", "GLOSSARY", "NOTE", "BODY",
			"PREVIOUS", "FINDINGS", "VERBATIM"} {
			if strings.Contains(text, "{{"+name+"}}") {
				t.Errorf("ask %d still has {{%s}} in it", i, name)
			}
		}
	}
}

func TestTheHashIsTheInstructionsAndNotThePiece(t *testing.T) {
	a := Ask{Chunk: markdown("one page of prose\n")}
	b := Ask{Chunk: markdown("a completely different page\n"), Glossary: "release  ->  bản phát hành"}
	ha, err := Hash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := Hash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Error("two pieces asked under the same rules got different hashes")
	}
	// The front matter is asked under different rules, so it hashes
	// differently and an edit to one does not restate the other.
	c := markdown("---\ntitle: A\n---\n")
	c.Part = chunk.PartFrontMatter
	hc, err := Hash(Ask{Chunk: c})
	if err != nil {
		t.Fatal(err)
	}
	if hc == ha {
		t.Error("the front matter and the body share a prompt hash")
	}
}

func TestTheSharedHalfIsTheSameOnEveryRequest(t *testing.T) {
	// The system half is what the prompt cache key is derived from, and it is
	// identical on all 2706 requests of a run. A per page sentence that leaked
	// above the split line would make every one of them unique and turn the
	// cache off with nothing failing to say so.
	a := markdown("one page\n")
	a.Rel, a.Index, a.Total, a.Heading = "ref/mod.md", 4, 60, "Module paths"
	b := markdown("a different page entirely\n")
	b.Rel, b.Kind = "doc/faq.md", content.KindMarkdown

	first, firstInput, err := (Ask{Chunk: a, Glossary: "release  ->  bản phát hành"}).Messages()
	if err != nil {
		t.Fatal(err)
	}
	second, secondInput, err := (Ask{Chunk: b}).Messages()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("the shared half of the prompt differs between two requests")
	}
	if firstInput == secondInput {
		t.Error("the per piece half is the same for two different pieces")
	}
	// And everything that is about one piece is below the line.
	for _, leaked := range []string{"ref/mod.md", "piece 4 of 60", "Module paths", "bản phát hành"} {
		if strings.Contains(first, leaked) {
			t.Errorf("%q is in the shared half of the prompt", leaked)
		}
		if !strings.Contains(firstInput, leaked) {
			t.Errorf("%q is in neither half of the prompt", leaked)
		}
	}
}

func TestBodyFindsNothingInSomethingThatIsNotARequest(t *testing.T) {
	if _, ok := Body("just some text"); ok {
		t.Error("found a source in a string with no fence in it")
	}
	if _, ok := Body("some instructions\n==========\nonly one fence\n"); ok {
		t.Error("found a source with only one fence")
	}
}
