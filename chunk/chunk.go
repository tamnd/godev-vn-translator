// Package chunk cuts a page into pieces small enough to ask for in one call.
//
// The corpus is 680 files and 34853 blocks, and the shape of it is lopsided.
// Half the blocks are under 84 bytes and nine in ten are under 406, so most
// pages go in one request and the cutting never runs. The other end is where
// the work is: ref/mod.md is 221 KB, doc/devel/weekly.html is 301 KB, and
// _content/blog/survey2017/background.html has a single block in it of 43411
// bytes. A file that does not fit has to be asked in pieces, and every decision
// in here is about which pieces are safe to cut between.
//
// Two properties hold everything else up. Concatenating the Text of every chunk
// of a file reproduces that file byte for byte, so putting the translation back
// together is a join and never a merge. And a cut never lands inside something
// that has to survive whole: not inside a fenced code block, not inside an HTML
// <pre>, not in the middle of a template definition. Those are the things the
// gates in quality/ compare element by element, and a chunk boundary through
// the middle of one produces two halves that each look wrong on their own.
package chunk

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tamnd/godev-vn-translator/content"
)

// DefaultBudget is how many bytes of English go in one request.
//
// It is a budget on the source and not on the answer, because the answer is the
// thing that is not known yet. Vietnamese runs longer than English for the same
// meaning, so the reply to a 6000 byte chunk is comfortably inside what a single
// completion returns without the model starting to compress to fit.
//
// 6000 was picked off the block distribution rather than off a token limit. At
// that size 99.9 percent of blocks in the corpus fit in one chunk with room
// around them, so the packer is nearly always fitting whole sections together
// rather than splitting anything. Raising it to 12000 would halve the number of
// calls on the long files and buy a much worse failure: the corpus already has
// eight files that stop early with no marker in them, and a truncated answer is
// the defect that is hardest to see and most expensive to find later.
const DefaultBudget = 6000

// Part says which half of a file a chunk came out of.
//
// The front matter is separated from the body because it is not prose and is
// not asked for in the same way. It is a fixed set of keys in a fixed order,
// some of whose values are translated and some of which are copied, and the
// gate on it (L09) refuses on the key list, the key order and the value of any
// of the verbatim keys. Sending it inside the first body chunk is how a file
// ends up carrying a `template: true` its English does not have, which is a
// mistake the corpus currently contains 138 times.
type Part string

const (
	PartFrontMatter Part = "front matter"
	PartBody        Part = "body"
)

// A Chunk is one request.
type Chunk struct {
	// Rel is the path under _content, which is the identity of the page
	// everywhere else: in the queue, in the manifest and in a report.
	Rel  string
	Kind content.Kind
	Part Part

	// Index is one based over the whole file and Total is how many pieces there
	// are. Both are put in the prompt. A model told it is on piece 3 of 7 does
	// not write a closing sentence, and one told it is on piece 1 of 1 does not
	// leave the last paragraph off waiting for a piece that is not coming.
	Index, Total int

	// Text is the English exactly as it stands in the file, trailing newlines
	// and all. Concatenating the Text of every chunk reproduces the file.
	Text string

	// Heading is the last heading at or above the start of this chunk, empty
	// when there is none above it yet. It is context for the model: a chunk cut
	// out of the middle of a page has no title on it, and a translator that
	// cannot tell whether it is reading a tutorial or a release note picks the
	// wrong register. Nothing else reads this.
	Heading string

	// Verbatim marks a chunk that is copied through rather than asked. See
	// verbatim below for what earns it.
	Verbatim bool

	// Split marks a chunk that begins or ends in the middle of a block, because
	// the block on its own was over budget. The prompt for one of these says so,
	// since a piece that starts mid-paragraph has to not be given an opening.
	Split bool
}

// Verbatim chunks are copied and not translated.
//
// The rule is inline SVG, and it is narrow on purpose. 66 units in the corpus
// carry an <svg> element and are over 2000 bytes, they total 600625 bytes, and
// every one of them is a chart inlined into one of the 2016 and 2017 survey
// posts or into blog/swisstable.md. Sending 600 KB of path data to a model with
// an instruction to give it back unchanged spends hours of fleet time on the
// one job a copy does perfectly, and every one of those requests is a chance to
// come back with a coordinate altered.
//
// The cost is real and worth stating plainly: the axis and legend labels inside
// those charts stay in English. They are <text> elements at computed positions,
// laid out for the English string, and a longer Vietnamese label runs off the
// plot. Redrawing the charts is a separate job from translating the prose and
// it is not this tool's.
//
// The floor of 2000 bytes is what keeps a small inline icon out of this. Four
// units in the corpus have an <svg> in them and are under it, and those are
// asked like anything else, because a decorative icon sitting in a sentence
// carries a <title> that a reader hears.
const (
	verbatimMark = "<svg"
	verbatimMin  = 2000
)

func verbatim(unit string) bool {
	return len(unit) >= verbatimMin && strings.Contains(strings.ToLower(unit), verbatimMark)
}

// frontMatterRE matches the whole leading fence, delimiters included, so the
// front matter chunk holds the ---, the YAML and the closing --- as they stand.
// Keeping the delimiters in the chunk is what makes the concatenation exact.
var frontMatterRE = regexp.MustCompile(`(?s)\A---\r?\n.*?\r?\n---[ \t]*\r?\n`)

// Split cuts one file up. budget of zero or less means DefaultBudget.
func Split(rel string, kind content.Kind, text string, budget int) []Chunk {
	if budget <= 0 {
		budget = DefaultBudget
	}
	var out []Chunk
	body := text
	if m := frontMatterRE.FindString(text); m != "" {
		out = append(out, Chunk{Rel: rel, Kind: kind, Part: PartFrontMatter, Text: m})
		body = text[len(m):]
	}

	heading := ""
	for _, group := range pack(units(body, kind), kind, budget) {
		c := Chunk{
			Rel: rel, Kind: kind, Part: PartBody,
			Text: group.text, Heading: heading, Verbatim: group.verbatim, Split: group.split,
		}
		if h := lastHeading(group.text, kind); h != "" {
			heading = h
		}
		out = append(out, c)
	}
	// An empty file still gets one chunk, because a caller that asked for a
	// file and got nothing back has no way to tell that from a file it forgot.
	if len(out) == 0 {
		out = append(out, Chunk{Rel: rel, Kind: kind, Part: PartBody, Text: text})
	}
	for i := range out {
		out[i].Index, out[i].Total = i+1, len(out)
	}
	return out
}

// Assemble puts the answers back in file order.
//
// It takes a map and not a slice because the answers do not arrive in order.
// Each chunk is a job in the queue, they run on whatever route is free, and one
// of them can fail and be asked again an hour after its neighbours were written.
// A missing index is an error and not a gap: a file assembled out of five
// answers where six were asked is exactly the silent truncation the corpus
// already has eight of, and it would be written to disk looking complete.
func Assemble(chunks []Chunk, answers map[int]string) (string, error) {
	var out strings.Builder
	for _, c := range chunks {
		if c.Verbatim {
			out.WriteString(c.Text)
			continue
		}
		answer, ok := answers[c.Index]
		if !ok {
			return "", fmt.Errorf("%s: no answer for piece %d of %d", c.Rel, c.Index, c.Total)
		}
		out.WriteString(answer)
	}
	return out.String(), nil
}

// group is one piece of the body, with its trailing blank lines attached so
// that a join is exact.
type group struct {
	text     string
	verbatim bool
	split    bool
	// atomic means this piece is never cut at line boundaries, however far over
	// budget it runs. A template definition is atomic: half of an {{if}} is not
	// a smaller template, it is a broken one. It says nothing about packing.
	// talks/slide.tmpl is 5605 bytes in 16 definitions, and asking about it in
	// 16 requests instead of one would be sixteen times the fleet time for a
	// file that fits in a single one.
	atomic bool
}

// holder tracks whether a scan is inside something a cut must not land in.
//
// Two scanners need this and they need the same answer from it: the one that
// finds unit boundaries, and the one that cuts an oversized unit at line
// boundaries. When they disagreed, a fence with a blank line in it survived the
// first and was cut in half by the second.
type holder struct {
	fence string
	tag   bool
}

// open reports whether a cut is allowed before the next line.
func (h *holder) open() bool { return h.fence == "" && !h.tag }

// advance takes one line into account.
func (h *holder) advance(line string) {
	trimmed := strings.TrimSpace(line)
	switch {
	case h.fence != "":
		if strings.HasPrefix(trimmed, h.fence) && strings.Trim(trimmed, h.fence[:1]) == "" {
			h.fence = ""
		}
	case h.tag:
		if htmlCloseRE.MatchString(line) {
			h.tag = false
		}
	default:
		if m := fenceOpenRE.FindStringSubmatch(line); m != nil {
			h.fence = m[1]
		} else if htmlHoldRE.MatchString(line) && !htmlCloseRE.MatchString(line) {
			h.tag = true
		}
	}
}

var (
	fenceOpenRE = regexp.MustCompile("^\\s*(```+|~~~+)")
	// htmlHoldRE is the elements whose content must not be cut. <pre> is the
	// one that matters: doc/effective_go.html has 148 of them and 36 blank
	// lines inside them, so a splitter that only knows about blank lines cuts
	// 36 code samples in half.
	htmlHoldRE  = regexp.MustCompile(`(?i)<(pre|script|style|textarea)\b`)
	htmlCloseRE = regexp.MustCompile(`(?i)</(pre|script|style|textarea)\s*>`)
	defineRE    = regexp.MustCompile(`^\{\{-?\s*define\b`)
)

// units cuts the body at the boundaries it is allowed to cut at.
//
// For everything except a template that boundary is a blank line: the first
// non-blank line after one starts a new unit, and the blank lines themselves
// stay on the end of the unit before, so joining the units back gives the body
// unchanged. Lines inside a fenced code block or inside one of the HTML
// elements in htmlHoldRE are not boundaries however blank they are.
//
// A template is different and is cut only at a top level {{define}}. site.tmpl
// is 20 KB and has 8 of them, talks/slide.tmpl has 16. Cutting a template at a
// blank line puts the two halves of an {{if}} in different requests, and the
// half with the {{end}} in it looks to a model like a fragment it should close.
func units(body string, kind content.Kind) []group {
	if body == "" {
		return nil
	}
	template := kind == content.KindTemplate
	var out []group
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			text := strings.Join(cur, "")
			out = append(out, group{text: text, verbatim: verbatim(text), atomic: template})
			cur = nil
		}
	}
	var hold holder
	prevBlank := true
	for _, line := range strings.SplitAfter(body, "\n") {
		trimmed := strings.TrimSpace(line)
		boundary := prevBlank && trimmed != ""
		if template {
			boundary = defineRE.MatchString(line)
		}
		if hold.open() && boundary && len(cur) > 0 {
			flush()
		}
		cur = append(cur, line)
		hold.advance(line)
		prevBlank = trimmed == ""
	}
	flush()
	return out
}

// pack fills chunks with units, and never puts a unit in two chunks.
//
// Greedy, with one preference on top: a unit that opens a section starts a new
// chunk once the one being filled is a third full. A section boundary is the
// best place a cut can land, because the heading that follows it tells the
// model what the next few paragraphs are about, and the paragraph before it has
// already finished saying what it was saying.
//
// A unit over the budget on its own is the case that has no good answer. It
// cannot be joined with anything, and cutting it at a line boundary produces a
// piece that begins mid-sentence. Both of those are bad, so the rule is: send
// it whole up to twice the budget, and cut at line boundaries beyond that,
// marking every piece so the prompt can say what it is. Twice the budget is
// where the risk crosses over, because at that size a truncated answer becomes
// more likely than a bad seam, and a truncation is invisible while a seam is
// not.
func pack(us []group, kind content.Kind, budget int) []group {
	var out []group
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, group{text: cur.String()})
			cur.Reset()
		}
	}
	for _, u := range us {
		if u.verbatim {
			flush()
			out = append(out, u)
			continue
		}
		if len(u.text) > 2*budget && !u.atomic {
			flush()
			out = append(out, cutLines(u.text, budget)...)
			continue
		}
		if cur.Len() > 0 {
			tooBig := cur.Len()+len(u.text) > budget
			section := opensSection(u.text, kind) && cur.Len() >= budget/3
			if tooBig || section {
				flush()
			}
		}
		cur.WriteString(u.text)
	}
	flush()
	return out
}

// cutLines is the last resort, for a single block bigger than two requests.
//
// It holds to the same rule as the boundary scanner: a line inside a fence or
// inside a <pre> is not a place to cut, however far over budget the piece has
// run. A block that is one enormous fence therefore comes out whole, which is
// the right answer, since a fence has almost no prose in it to translate.
func cutLines(text string, budget int) []group {
	var out []group
	var cur strings.Builder
	var hold holder
	for _, line := range strings.SplitAfter(text, "\n") {
		if hold.open() && cur.Len() > 0 && cur.Len()+len(line) > budget {
			out = append(out, group{text: cur.String(), split: true})
			cur.Reset()
		}
		cur.WriteString(line)
		hold.advance(line)
	}
	if cur.Len() > 0 {
		out = append(out, group{text: cur.String(), split: true})
	}
	return out
}

var (
	atxRE   = regexp.MustCompile(`^#{1,6}\s`)
	htmlHRE = regexp.MustCompile(`(?i)^\s*<h[1-6][\s>]`)
	tagRE   = regexp.MustCompile(`<[^>]*>`)
	// presentRE is a section heading in the present format the tour and the
	// talks are written in, where a line starting with a star is a slide title.
	// It is only a heading in those two kinds. In Markdown the same line is a
	// bullet.
	presentRE = regexp.MustCompile(`^\*+\s`)
)

func opensSection(unit string, kind content.Kind) bool {
	line, _, _ := strings.Cut(unit, "\n")
	if atxRE.MatchString(line) || htmlHRE.MatchString(line) {
		return true
	}
	return (kind == content.KindArticle || kind == content.KindSlide) && presentRE.MatchString(line)
}

// lastHeading reads the title off a chunk so the next one can be told where it
// is. The attribute block is dropped: {#type-params} is an anchor and telling a
// model it is reading a section called "Type parameters {#type-params}" invites
// it to write one back.
func lastHeading(text string, kind content.Kind) string {
	out := ""
	for line := range strings.SplitSeq(text, "\n") {
		if !opensSection(line+"\n", kind) {
			continue
		}
		h := tagRE.ReplaceAllString(strings.TrimSpace(line), "")
		h = strings.TrimLeft(h, "#* \t")
		if i := strings.Index(h, "{#"); i >= 0 {
			h = strings.TrimSpace(h[:i])
		}
		if h != "" {
			out = h
		}
	}
	return out
}
