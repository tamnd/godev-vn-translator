// Package prompt builds the request that is sent to a model.
//
// The instructions are Markdown files rather than string literals in Go, for
// two reasons. They are prose and they are edited as prose, by somebody reading
// them end to end and arguing with a sentence, which is not what editing a
// quoted string in a source file feels like. And they are embedded rather than
// read off disk, so a binary carries the exact text it was built with and a run
// cannot pick up half an edit.
//
// Every set of instructions has a hash, and that hash is recorded next to every
// file it produced. That is what makes a prompt change visible: when a rule in
// here is tightened, the pages translated under the old rules are detectably
// old rather than silently mixed in with the new ones. It is also the thing to
// be careful with, because moving a hash puts every page back in the queue. A
// sentence added to translate.md costs the whole corpus, so a rule about one
// kind of file belongs in the file for that kind.
//
// The other half of the design is what is deliberately not sent. The glossary
// is cut down to the terms the piece actually contains before it gets here, and
// nothing in this package adds the rest of it back. The full table is 35 rows
// today and will be several hundred when the site is covered, and sending all
// of it with every request spends the context that the long pages need most on
// terms that are not in them.
package prompt

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/tamnd/godev-vn-translator/chunk"
	"github.com/tamnd/godev-vn-translator/content"
)

//go:embed translate.md
var translateInstructions string

//go:embed frontmatter.md
var frontMatterInstructions string

//go:embed repair.md
var repairInstructions string

//go:embed vietnamese.md
var vietnameseRules string

// fence is the pair of lines the source is put between.
//
// A model reading a request has no other way to tell the instructions from the
// text it is being asked about, and the text is documentation, which means it
// is full of sentences in the imperative addressed to a reader. "Run go build
// -cover to compile the program" is a line of the corpus and it is also exactly
// what an injected instruction looks like. The fence and the sentence above it
// saying that nothing between the lines is an instruction are what keep those
// apart.
const fence = "\n=========="

// split is the line that separates the two halves of a prompt file.
//
// Above it is the part that is the same on every request, and it is sent as the
// system message. Below it is the part that is about this one piece, and it is
// sent as the user message. The line is in the file rather than the split being
// done in Go, so somebody editing the prose can see where the cut is and can
// move a paragraph across it.
//
// The reason for the cut is the prompt cache. The shared half is around four
// thousand characters and it is identical on all 2706 requests of a run, so it
// is what the cache key is derived from; the half below it changes every time.
// Putting a per page sentence above the line would make every request's system
// message unique and turn the cache off without anything failing to say so.
const split = "\n%%%%%\n"

// SHA256 is the digest used everywhere in this repo, hex, of the whole text.
func SHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// An Ask is one request, before it is turned into text.
type Ask struct {
	// Chunk is the piece of the page being asked about.
	Chunk chunk.Chunk
	// Glossary is the terminology block, already cut down to the terms this
	// piece contains. Empty when the piece contains none of them, which is
	// ordinary: half the chunks in the corpus are shorter than a paragraph.
	Glossary string
	// Note is anything else worth telling the model about this one piece. It is
	// the escape hatch for a fact about a single page, and it is deliberately
	// not a place to put a rule, because a rule put here is a rule that applies
	// to one page and is forgotten.
	Note string

	// Previous and Findings turn a translation into a repair. Previous is what
	// came back last time and Findings is what the gates said about it, one to
	// a line, as the report writes them.
	Previous string
	Findings []string
}

// Repair reports whether this ask is a second attempt at a piece that failed.
func (a Ask) Repair() bool { return a.Previous != "" && len(a.Findings) > 0 }

// Text is the whole request as one piece, for reading and for a test.
func (a Ask) Text() (string, error) {
	instructions, input, err := a.Messages()
	if err != nil {
		return "", err
	}
	return instructions + "\n" + input, nil
}

// Messages is the request as it is actually sent: the shared instructions, then
// the part about this one piece.
func (a Ask) Messages() (instructions, input string, err error) {
	file, err := instructionsFor(a)
	if err != nil {
		return "", "", err
	}
	above, below, ok := strings.Cut(file, split)
	if !ok {
		return "", "", fmt.Errorf("prompt for %s has no %s line in it",
			a.Chunk.Rel, strings.TrimSpace(split))
	}
	instructions = strings.TrimSpace(above)
	instructions = strings.ReplaceAll(instructions, "{{VERBATIM}}", verbatimKeys())
	instructions = strings.ReplaceAll(instructions, "{{RULES}}", strings.TrimSpace(vietnameseRules))

	input = strings.TrimSpace(below)
	input = strings.ReplaceAll(input, "{{WHERE}}", where(a.Chunk))
	input = strings.ReplaceAll(input, "{{GLOSSARY}}", glossaryBlock(a.Glossary))
	input = strings.ReplaceAll(input, "{{NOTE}}", note(a.Note))
	input = strings.ReplaceAll(input, "{{PREVIOUS}}", strings.TrimRight(a.Previous, "\n"))
	input = strings.ReplaceAll(input, "{{FINDINGS}}", findings(a.Findings))
	// The body goes in last, and with no trimming of its leading whitespace.
	// A chunk that starts with an indented line starts with an indented line,
	// because in Markdown four spaces is a code block.
	input = strings.ReplaceAll(input, "{{BODY}}", strings.TrimRight(a.Chunk.Text, "\n"))
	return instructions + "\n", input + "\n", nil
}

// Hash identifies the instructions an ask was built from, with everything
// specific to the piece taken out.
//
// It is what goes in the manifest beside a translated file. Two files with the
// same hash were asked for under the same rules; a file whose hash is not the
// current one was asked for under rules that have since changed. The glossary
// is not in it, on purpose. The glossary changes when somebody adds a term, the
// terms in it are already checked by a gate that reads the file on disk rather
// than a recorded hash, and folding it in here would put the whole corpus back
// in the queue every time a row is added.
func Hash(a Ask) (string, error) {
	instructions, err := instructionsFor(a)
	if err != nil {
		return "", err
	}
	// Both halves, because a rule can live in either one and a change to either
	// one changes what the answer should look like.
	text := strings.TrimSpace(instructions)
	text = strings.ReplaceAll(text, "{{RULES}}", strings.TrimSpace(vietnameseRules))
	text = strings.ReplaceAll(text, "{{VERBATIM}}", verbatimKeys())
	return SHA256(text + "\n"), nil
}

func instructionsFor(a Ask) (string, error) {
	switch {
	case a.Chunk.Verbatim:
		// Nothing is asked about a piece that is copied. A caller that gets
		// here has lost track of which pieces it is sending, and the failure it
		// would otherwise cause is a chart coming back rebuilt.
		return "", fmt.Errorf("%s piece %d is copied through and is not asked for",
			a.Chunk.Rel, a.Chunk.Index)
	case a.Repair():
		return repairInstructions, nil
	case a.Chunk.Part == chunk.PartFrontMatter:
		return frontMatterInstructions, nil
	default:
		return translateInstructions, nil
	}
}

// where tells the model what it is holding.
//
// A chunk cut out of the middle of a long page arrives with no title on it and
// no idea what came before it, and a translator that cannot tell a release note
// from a tutorial picks the wrong register for both. This is also where the
// piece is told not to write an ending, which is the failure that produces a
// file with a closing paragraph in the middle of it.
func where(c chunk.Chunk) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The page is %s and it is %s.", c.Rel, describe(c.Kind))
	if c.Total > 1 {
		fmt.Fprintf(&b, " This is piece %d of %d.", c.Index, c.Total)
		switch {
		case c.Index == 1:
			b.WriteString(" More of the page follows it, so do not write an ending.")
		case c.Index == c.Total:
			b.WriteString(" It is the last piece, so it ends where the page ends.")
		default:
			b.WriteString(" There is a piece before it and a piece after it," +
				" so do not write an opening and do not write an ending.")
		}
	}
	if c.Heading != "" {
		fmt.Fprintf(&b, " It sits under the heading %q.", c.Heading)
	}
	if c.Split {
		b.WriteString(" It begins or ends in the middle of a block, because the block" +
			" was too long to ask for at once. Do not close anything you did not open" +
			" and do not open anything you do not close.")
	}
	return b.String()
}

// describe is the one sentence about a file kind that the shared instructions
// cannot carry, because it is different for each of them.
func describe(k content.Kind) string {
	switch k {
	case content.KindMarkdown:
		return "Markdown"
	case content.KindHTML:
		return "HTML, so the tags and the attributes are structure and only the text" +
			" between them is prose"
	case content.KindArticle, content.KindSlide:
		return "the present format, where a line beginning with a star is a section" +
			" title, a line beginning with a dot is a command the tool runs, and" +
			" an indented block is code"
	case content.KindTemplate:
		return "a Go template, so nearly all of it is code: the prose is the text" +
			" between the tags and the occasional quoted label, and everything" +
			" between two braces is copied exactly"
	case content.KindYAML:
		return "YAML, so the keys are structure and are never translated," +
			" the indentation is structure, and only the values a reader sees" +
			" are prose"
	}
	return string(k)
}

// verbatimKeys is the front matter fields the site reads as data, taken from
// the gate that refuses when one of them has changed, so the instruction and
// the check cannot drift apart.
func verbatimKeys() string {
	var b strings.Builder
	for _, key := range content.VerbatimKeys {
		fmt.Fprintf(&b, "  %s\n", key)
	}
	return strings.TrimRight(b.String(), "\n")
}

// glossaryBlock is the terminology, or a sentence saying there is none.
//
// A heading with an empty list under it reads to a model as a list that failed
// to arrive, and short chunks with no glossary terms in them are the common
// case rather than the odd one.
func glossaryBlock(g string) string {
	if strings.TrimSpace(g) == "" {
		return "No term in the glossary appears in this piece."
	}
	return strings.TrimSpace(g)
}

func note(n string) string {
	if strings.TrimSpace(n) == "" {
		return ""
	}
	return "\n" + strings.TrimSpace(n) + "\n"
}

func findings(list []string) string {
	if len(list) == 0 {
		return "Nothing."
	}
	var b strings.Builder
	for _, f := range list {
		fmt.Fprintf(&b, "  %s\n", strings.TrimSpace(f))
	}
	return strings.TrimRight(b.String(), "\n")
}

// Body pulls the source back out of a request.
//
// For a caller holding a recorded ask and not the chunk that made it, which is
// what a failed job in the queue is: it carries the request it sent, and the
// piece of English inside it is the thing somebody reading the failure wants to
// see first.
func Body(ask string) (string, bool) {
	i := strings.Index(ask, fence)
	if i < 0 {
		return "", false
	}
	i += len(fence)
	j := strings.Index(ask[i:], fence)
	if j < 0 {
		return "", false
	}
	return strings.TrimSpace(ask[i : i+j]), true
}
