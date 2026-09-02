package content

import (
	"regexp"
	"strings"
)

// A Document is one file taken apart into the pieces the gates compare.
//
// Nothing here is a full Markdown parse. The gates do not need a tree, they
// need the handful of things a model gets wrong: it drops a paragraph, it
// renumbers a heading, it edits a line of Go inside a code block, it loses a
// link, it translates the name of a template function. Each of those is a
// sequence that has to survive translation, and each is extracted here as a
// sequence so a comparison is a comparison of two slices.
type Document struct {
	// FrontMatter is the YAML between the first two --- lines, when there is
	// one. The site reads title, date, by, tags, summary, layout, template and
	// redirect out of it.
	FrontMatter string
	// Body is everything after it.
	Body string

	Blocks   []string
	Headings []Heading
	Fences   []Fence
	Links    []Link
	Actions  []string
}

// Heading is one ATX heading.
type Heading struct {
	Level int
	// Text is the heading without its hashes and without its attribute block.
	// It is prose and it is expected to change.
	Text string
	// ID is the explicit identifier from a {#id} attribute block, empty when
	// the heading has none.
	//
	// It is the single most important thing on a heading in a translated site.
	// A heading with no explicit id gets one derived from its own text, so the
	// English "Should you constrain to pointer receivers?" and its Vietnamese
	// are two different anchors, and every link into that section from
	// anywhere else breaks the moment the section is translated. An explicit
	// id is the same on both sides and the link survives.
	ID string
	// Attrs is the whole attribute block, kept so a gate can say the class
	// list changed as well as the id.
	Attrs string
	Line  int
}

// Fence is one fenced code block.
type Fence struct {
	// Info is the word after the backticks, which selects the highlighter.
	Info string
	Body string
	Line int
}

// Link is one inline Markdown link or image.
type Link struct {
	// Text is the label, which is prose.
	Text string
	// Target is the URL or the anchor, which is not.
	Target string
	// Title is the optional quoted string after the target, which is prose:
	// ref/mod.md carries five of them and all five are correctly Vietnamese.
	Title string
	Image bool
	Line  int
}

var (
	frontMatterRE = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n`)
	headingRE     = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*$`)
	attrsRE       = regexp.MustCompile(`\s*\{([^}]*)\}\s*$`)
	idRE          = regexp.MustCompile(`#([^\s}]+)`)
	fenceOpenRE   = regexp.MustCompile("^(\\s*)(```+|~~~+)\\s*(\\S*)")
	actionRE      = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
)

// Parse takes a file apart.
func Parse(text string) Document {
	var doc Document
	doc.Body = text
	if m := frontMatterRE.FindStringSubmatch(text); m != nil {
		doc.FrontMatter = m[1]
		doc.Body = text[len(m[0]):]
	}
	doc.Fences = fences(doc.Body)
	doc.Headings = headings(doc.Body)
	doc.Links = linksOf(doc.Body)
	doc.Actions = actionRE.FindAllString(text, -1)
	doc.Blocks = blocks(doc.Body)
	return doc
}

// fences pulls out the code blocks. Everything inside one is exempt from the
// prose gates and subject to the code gate, so this has to run before anything
// else looks at the body.
func fences(body string) []Fence {
	var out []Fence
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		m := fenceOpenRE.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		marker, indent := m[2], m[1]
		var buf []string
		j := i + 1
		for ; j < len(lines); j++ {
			trimmed := strings.TrimSpace(lines[j])
			if strings.HasPrefix(trimmed, marker[:1]) && strings.HasPrefix(trimmed, marker) &&
				strings.TrimRight(trimmed, string(marker[0])) == "" {
				break
			}
			buf = append(buf, strings.TrimPrefix(lines[j], indent))
		}
		out = append(out, Fence{Info: m[3], Body: strings.Join(buf, "\n"), Line: i + 1})
		i = j
	}
	return out
}

// insideFence marks the lines of the body that are code, so headings and links
// written in an example are not counted as the document's own.
func insideFence(body string) []bool {
	lines := strings.Split(body, "\n")
	in := make([]bool, len(lines))
	marker := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if marker == "" {
			if m := fenceOpenRE.FindStringSubmatch(line); m != nil {
				marker = m[2]
				in[i] = true
			}
			continue
		}
		in[i] = true
		if strings.HasPrefix(trimmed, marker) && strings.TrimRight(trimmed, string(marker[0])) == "" {
			marker = ""
		}
	}
	return in
}

func headings(body string) []Heading {
	var out []Heading
	code := insideFence(body)
	for i, line := range strings.Split(body, "\n") {
		if code[i] {
			continue
		}
		m := headingRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		h := Heading{Level: len(m[1]), Text: m[2], Line: i + 1}
		if a := attrsRE.FindStringSubmatch(h.Text); a != nil {
			h.Attrs = strings.TrimSpace(a[1])
			h.Text = strings.TrimSpace(h.Text[:len(h.Text)-len(a[0])])
			if id := idRE.FindStringSubmatch(h.Attrs); id != nil {
				h.ID = id[1]
			}
		}
		out = append(out, h)
	}
	return out
}

// linkRE matches [text](target) and ![text](target), with an optional quoted
// title. The target is taken as everything up to a space or the closing
// bracket, which is what separates /doc/mvs/upgrade.svg from the "MVS upgrade"
// that follows it.
var linkRE = regexp.MustCompile(`(!?)\[([^\]]*)\]\(\s*([^\s)]*)\s*(?:"([^"]*)")?\s*\)`)

func linksOf(body string) []Link {
	var out []Link
	code := insideFence(body)
	for i, line := range strings.Split(body, "\n") {
		if code[i] {
			continue
		}
		for _, m := range linkRE.FindAllStringSubmatch(line, -1) {
			out = append(out, Link{
				Image: m[1] == "!", Text: m[2], Target: m[3], Title: m[4], Line: i + 1,
			})
		}
	}
	return out
}

// blocks counts what the Markdown calls a block: runs of non-blank lines,
// with a fenced code block counting as one however many blank lines are in it.
//
// It is the truncation detector. blog/contributors-summit-2019.md has 53 of
// them in English and 34 in the Vietnamese, and the file simply stops in the
// middle of a section with no sign in it that anything is missing. Nothing else
// in this package would have caught that: the headings that survived match, the
// links that survived match, and the prose reads well right up to where it ends.
func blocks(body string) []string {
	var out []string
	var cur []string
	code := insideFence(body)
	lines := strings.Split(body, "\n")
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" && !code[i] {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

// VerbatimKeys are the front matter fields copied through untranslated.
//
// A date is a date. `by` is a list of people's names, and translating a name is
// a bug. `tags` are matched against each other across posts to build the tag
// index, so a translated tag silently empties a page. `layout`, `template` and
// `redirect` are read by the site's Go code and mean nothing else.
//
// It lives here rather than with the gate that enforces it because two things
// need it and they must not disagree: the gate that refuses a file whose `date`
// has moved, and the instruction that tells a model not to move it. A list kept
// in two places is a list that drifts, and it drifts in the direction where the
// model is told one thing and judged by another.
var VerbatimKeys = []string{"date", "by", "tags", "layout", "template", "redirect", "series"}

// FrontMatterKeys lists the top level keys of the front matter in the order
// they are written, which is the order a gate compares them in.
func FrontMatterKeys(frontMatter string) []string {
	var out []string
	for line := range strings.SplitSeq(frontMatter, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") ||
			strings.HasPrefix(line, "-") || strings.TrimSpace(line) == "" {
			continue
		}
		key, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out = append(out, strings.TrimSpace(key))
	}
	return out
}

// FrontMatterValue returns the raw value of one top level key.
func FrontMatterValue(frontMatter, key string) (string, bool) {
	for line := range strings.SplitSeq(frontMatter, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}
