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
	Comments []string
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
	// jsonFrontMatterRE is the other form, and the corpus has 77 files in it
	// against 603 in the one above. `doc/`, `ref/` and `solutions/` write their
	// front matter as a JSON object inside an HTML comment, which is what
	// golang.org/x/website/internal/web reads there.
	//
	// Nothing here knew about it until doc/contrib.md came back from a run with
	// the tab in front of "Redirect" turned into a space. That file is four lines
	// long and three of them are the comment, so there was nothing else the model
	// could have touched. The front matter was never separated into its own
	// chunk, so it went out inside the body as if it were prose, and L09 had
	// never looked at any of the 77 because FrontMatterKeys returned nothing for
	// all of them. A rule that reports nothing may be blind rather than satisfied.
	jsonFrontMatterRE = regexp.MustCompile(`(?s)\A<!--\{.*?\}-->[ \t]*\r?\n?`)
	headingRE     = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*$`)
	// presentHeadingRE is the same thing for present(1), which the .article and
	// .slide files are written in. A section there opens with a star and not a
	// hash, and a line that opens with a hash is a comment, which is the exact
	// opposite of what headingRE would make of it.
	//
	// The title is optional because present says it is. parseSections in
	// x/tools/present opens a section on `text == prefix || strings.HasPrefix(text,
	// prefix+" ")`, so a line holding nothing but stars is a section with an empty
	// title, and the slide decks use that for the ones that are a single full
	// bleed image. English writes those as a star and a couple of trailing spaces
	// and the Vietnamese wrote a bare star, which renders the same and which this
	// rule was calling a lost section. Three in talks/2013/oscon-dl.slide and one
	// in talks/2015/gophercon-goevolution.slide, and those two files were the whole
	// difference on the corpus.
	presentHeadingRE = regexp.MustCompile(`^(\*{1,4})(?:\s+(.*?))?\s*$`)
	attrsRE          = regexp.MustCompile(`\s*\{([^}]*)\}\s*$`)
	idRE             = regexp.MustCompile(`#([^\s}]+)`)
	fenceOpenRE      = regexp.MustCompile("^(\\s*)(```+|~~~+)\\s*(\\S*)")
	actionRE         = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
)

// Parse takes a file apart.
//
// The kind is needed for one thing only, and it is the thing that reads
// backwards. In Markdown a line opening with a hash is a heading; in present(1),
// which the .article and .slide files are written in, a line opening with a hash
// is a comment and a heading opens with a star. Parsing a slide as Markdown
// counts its comments as its structure, so a translator who rewrapped a
// four line comment into three lines looks like a translator who dropped a
// section. That is what L04 was saying about talks/2012/simple.slide.
//
// Everything else here is format agnostic on purpose. Present bodies carry
// Markdown links, fenced code and HTML comments, and the rules that count those
// hold on a slide exactly as they hold on a page.
func Parse(kind Kind, text string) Document {
	var doc Document
	doc.Body = text
	if m := frontMatterRE.FindStringSubmatch(text); m != nil {
		doc.FrontMatter = m[1]
		doc.Body = text[len(m[0]):]
	} else if m := jsonFrontMatterRE.FindString(text); m != "" {
		// The braces are kept, unlike the `---` fences above, because they are
		// what FrontMatterKeys reads the form off. The comment markers are not,
		// because they are the delimiter and the fences are not kept either.
		doc.FrontMatter = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(m), "<!--"), "-->")
		doc.Body = text[len(m):]
	}
	doc.Fences = fences(doc.Body)
	doc.Headings = headings(doc.Body, kind)
	doc.Links = linksOf(doc.Body)
	doc.Actions = actionRE.FindAllString(text, -1)
	doc.Blocks = blocks(doc.Body)
	doc.Comments = comments(doc.Body)
	return doc
}

var commentRE = regexp.MustCompile(`(?s)<!--(.*?)-->`)

// comments pulls out the HTML comments, which on this site are not remarks. A
// release note line is written under `<!-- CL 29072 -->` or
// `<!-- go.dev/issue/61405 -->`, which is how anyone reading it later gets from
// the sentence back to the change that caused it, and the `.html` pages carry
// `<!-- for consistent spacing -->` between inline elements, where the comment
// is what keeps the whitespace out of the rendering.
//
// The site's own page metadata is not a comment and is dropped here. An `.html`
// page under _content opens with `<!--{ "Title": ... }-->`, and Parse only
// knows how to lift YAML front matter out of the body, so that block is still
// in Body when this runs. It is the one comment on the site whose contents are
// meant to be translated, and leaving it in would make every translated .html
// page look like it had lost one. Nothing else on the corpus opens with a
// brace.
//
// Fenced code is blanked first, for the same reason links are: a comment in an
// example is not the page's own.
func comments(body string) []string {
	var out []string
	for _, m := range commentRE.FindAllStringSubmatch(blankFences(body), -1) {
		if strings.HasPrefix(strings.TrimSpace(m[1]), "{") {
			continue
		}
		out = append(out, m[1])
	}
	return out
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

func headings(body string, kind Kind) []Heading {
	re := headingRE
	if kind == KindArticle || kind == KindSlide {
		re = presentHeadingRE
	}
	var out []Heading
	code := insideFence(body)
	for i, line := range strings.Split(body, "\n") {
		if code[i] {
			continue
		}
		m := re.FindStringSubmatch(line)
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

// htmlLinkRE matches an href or src attribute on the elements that carry one.
//
// The 78 `.html` pages under _content are hand written HTML, not Markdown, so
// every link on them is an attribute and linkRE sees none of it. Without this
// L07 has been checking nothing at all on 55 translated pages carrying 772
// targets between them, which is how ten anchors reached _content_vi reading
// `href="[url](url)"`, a target that is not a url, without a gate objecting.
//
// The element list is closed on purpose. An `href` is also how `<link>` names a
// stylesheet and how `<base>` names a prefix, and a `src` belongs to a dozen
// things, but a rule that took any attribute called href anywhere would start
// reading the examples the page is teaching from. These six are the ones the
// corpus uses for a link a reader can follow or an asset the page needs.
//
// Double quotes only, which is what all 772 of them use.
var htmlLinkRE = regexp.MustCompile(`(?i)<(a|img|link|script|iframe|source)\b[^>]*?\b(href|src)="([^"]*)"`)

// linksOf reads the whole body at once rather than a line at a time, because
// the link text is allowed to wrap.
//
// doc/security/vuln/cna.md is where that turned up. The English writes
// `[standard library](/pkg)` on one line and the Vietnamese writes
//
//	([thư viện
//	chuẩn](/pkg) và
//
// which is the same link and renders the same, and a line at a time scan sees
// neither half of it. So L07 reported a dropped link on a page that drops
// nothing, and the repair prompt sent a model after a defect that was not
// there. A gate that cries wolf is worse than no gate, because the answer to it
// is to stop reading it.
//
// The prices of doing it this way are two. Fenced code has to be blanked out
// rather than skipped, and it is blanked byte for byte so that the offsets stay
// true and the reported line number is the line the link is really on. And the
// target still may not contain a space, so it cannot wrap. That is correct:
// Markdown does not allow it either.
func linksOf(body string) []Link {
	var out []Link
	text := blankFences(body)
	for _, m := range linkRE.FindAllStringSubmatchIndex(text, -1) {
		at := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return text[m[2*i]:m[2*i+1]]
		}
		out = append(out, Link{
			Image:  at(1) == "!",
			Text:   at(2),
			Target: at(3),
			Title:  at(4),
			Line:   strings.Count(text[:m[0]], "\n") + 1,
		})
	}
	// Then the HTML anchors, on the same blanked body so that an example
	// teaching HTML does not count as the page's own links. Text is left empty:
	// an anchor's label is between the tags and can hold other elements, a
	// regexp cannot take it out reliably, and no rule reads it. Target is the
	// whole of what L05 and L07 want.
	for _, m := range htmlLinkRE.FindAllStringSubmatchIndex(text, -1) {
		out = append(out, Link{
			Image:  strings.EqualFold(text[m[2]:m[3]], "img"),
			Target: text[m[6]:m[7]],
			Line:   strings.Count(text[:m[0]], "\n") + 1,
		})
	}
	return out
}

// blankFences replaces every byte of every fenced line with a space, keeping
// the newlines. The result is the same length as the body, so an offset into it
// is an offset into the body.
func blankFences(body string) string {
	code := insideFence(body)
	lines := strings.Split(body, "\n")
	for i := range lines {
		if !code[i] {
			continue
		}
		lines[i] = strings.Repeat(" ", len(lines[i]))
	}
	return strings.Join(lines, "\n")
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

// jsonKeyRE matches one top level key of the JSON form. Every value in the 77
// files is a string or a bool, none is an object or an array, so a line is
// either a key or a brace and there is no nesting to track. A value that ever
// grows an object would need a real parser, and this returns the outer key
// twice rather than quietly returning the inner one, which is a wrong answer
// that a test would see.
var jsonKeyRE = regexp.MustCompile(`(?m)^\s*"([^"]+)"\s*:`)

// isJSONFrontMatter reports the form. Parse keeps the braces on the JSON one
// and strips the fences off the YAML one, so the first character says which.
//
// The comment marker is allowed in front of it because chunk hands these
// functions the whole matched block, delimiters and all, which is what makes
// its concatenation exact. Both callers have to get the same answer.
func isJSONFrontMatter(frontMatter string) bool {
	s := strings.TrimSpace(frontMatter)
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "<!--{")
}

// FrontMatterKeys lists the top level keys of the front matter in the order
// they are written, which is the order a gate compares them in.
func FrontMatterKeys(frontMatter string) []string {
	if isJSONFrontMatter(frontMatter) {
		var out []string
		for _, m := range jsonKeyRE.FindAllStringSubmatch(frontMatter, -1) {
			out = append(out, m[1])
		}
		return out
	}
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
//
// The key is matched without regard to case, which changes nothing on the YAML
// form because every key there is lowercase and so is every name in
// VerbatimKeys. It is the JSON form that needs it: those files write `Redirect`
// and `Template`, and an exact match would have the verbatim value check in L09
// find no key and say nothing on all 77 of them, which is the same silence the
// rule was already keeping for a different reason. A key whose case moved
// between the English and the Vietnamese is caught by the key list check, which
// is exact, so nothing is lost by being lenient here.
func FrontMatterValue(frontMatter, key string) (string, bool) {
	if isJSONFrontMatter(frontMatter) {
		for _, m := range jsonKeyRE.FindAllStringSubmatchIndex(frontMatter, -1) {
			if !strings.EqualFold(frontMatter[m[2]:m[3]], key) {
				continue
			}
			rest := frontMatter[m[1]:]
			if i := strings.IndexByte(rest, '\n'); i >= 0 {
				rest = rest[:i]
			}
			return strings.TrimSuffix(strings.TrimSpace(rest), ","), true
		}
		return "", false
	}
	for line := range strings.SplitSeq(frontMatter, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}
