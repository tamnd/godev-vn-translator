package quality

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/tamnd/godev-vn-translator/content"
)

// L01. A translatable English file with no Vietnamese counterpart.
//
// The overlay filesystem in godev-vn serves the English when the Vietnamese is
// missing, which is the right behaviour for a reader and the wrong one for a
// maintainer: the gap is invisible. On the corpus as it stood after the
// upstream sync this found 26 files with no translation at all, 14 .html, 9 .md
// and 3 .yaml, and none of them had ever appeared in a report. The 9 Markdown
// files are the ones the sync brought in.
//
// It reports rather than refuses. A Vietnamese site that falls back to English
// on 26 pages is a site; one that refuses to build is not.
var rulePresence = Rule{
	ID: "L01", Name: "presence", Severity: Notice,
	Check: func(in Input) []Finding {
		if in.Pair.Exists {
			return nil
		}
		return []Finding{{Msg: fmt.Sprintf(
			"%s has no translation, so the site serves the English", in.Pair.Rel)}}
	},
}

// L02. The Vietnamese file is byte for byte the English one.
//
// 97 files on the corpus. A copied file is worse than a missing one: the
// overlay resolves it, so the page is English and the tree says it is done, and
// every count of coverage taken off the file listing is wrong by 97. Coverage
// in this package is 557 of 680 for that reason, and not 654 of 680.
var ruleUntranslated = Rule{
	ID: "L02", Name: "untranslated", Severity: Notice,
	Check: func(in Input) []Finding {
		if in.EN != in.VI {
			return nil
		}
		return []Finding{{Msg: "is a byte for byte copy of the English"}}
	},
}

// TruncationSlack is how much shorter the Vietnamese may be before L03 calls it
// truncated.
//
// Block for block, a translation is one in one out: a blank line is a block
// boundary in both languages and nothing about Vietnamese merges two
// paragraphs. So the honest threshold is zero, and it is not zero because a
// translator legitimately joins a two line bullet onto one line and splits a
// long sentence, and because Markdown's own block counting has edge cases
// around loose lists that this package does not model.
//
// One block, or two percent, whichever is larger. On the corpus that admits
// every file whose difference is a list reflow and catches eight real
// truncations: contributors-summit-2019.md at 52 blocks against 33,
// gomod-ref.md at 178 against 162, generic-interfaces.md at 113 against 100,
// fips140.md at 56 against 44, go1.23.md at 168 against 161, policy.md at 48
// against 44, swisstable.md at 60 against 56 and gofix.md at 68 against 65.
const TruncationSlack = 0.02

// L03. The translation stops early.
//
// This is the defect that made the package worth writing.
// blog/contributors-summit-2019.md is 397 lines of English and 167 lines of
// Vietnamese, and it does not end: it stops in the middle of the section on
// proposals, with no marker, no note and no half sentence to catch the eye. The
// prose that is there is good. Every heading that survived matches its English.
// Every link that survived resolves. A reader who does not have the English
// open has no way to know that the last four sections of the article are gone,
// and neither did anything in this repo until L03.
//
// A model does this when the passage is long: it translates until the answer
// gets big, writes a plausible closing sentence and stops. Nothing in the
// answer says so, which is why the check has to be a count taken from outside.
var ruleTruncation = Rule{
	ID: "L03", Name: "truncation", Severity: Refuse,
	Check: func(in Input) []Finding {
		en, vi := len(in.ENDoc.Blocks), len(in.VIDoc.Blocks)
		if en == 0 {
			return nil
		}
		slack := max(1, int(float64(en)*TruncationSlack))
		if en-vi <= slack {
			return nil
		}
		return []Finding{{Msg: fmt.Sprintf(
			"has %d blocks against the English %d, so %d are missing and the file stops early",
			vi, en, en-vi)}}
	},
}

// L04. The heading tree changed.
//
// Same count, same levels, same order. The text is prose and is expected to
// differ; everything else about a heading is structure the page's table of
// contents and its anchors are built from.
//
// Five files on the corpus: doc/modules/gomod-ref.md at 41 headings against 37,
// blog/contributors-summit-2019.md at 7 against 4 (the truncation again, seen
// from a second direction), doc/security/fips140.md at 11 against 10,
// talks/2015/gofmt-cn.slide at 205 against 204 and talks/2012/simple.slide at
// 7 against 5.
var ruleHeadings = Rule{
	ID: "L04", Name: "headings", Severity: Refuse,
	Check: func(in Input) []Finding {
		en, vi := in.ENDoc.Headings, in.VIDoc.Headings
		if len(en) != len(vi) {
			return []Finding{{Msg: fmt.Sprintf(
				"has %d headings against the English %d", len(vi), len(en))}}
		}
		var out []Finding
		for i := range en {
			if en[i].Level != vi[i].Level {
				out = append(out, Finding{Line: vi[i].Line, Msg: fmt.Sprintf(
					"heading %d is level %d and the English is level %d (%q)",
					i+1, vi[i].Level, en[i].Level, en[i].Text)})
			}
		}
		return out
	},
}

// L05. A heading lost its identifier, or a heading that is linked to has none.
//
// Two findings from one rule because they are two halves of one fact about a
// translated site.
//
// The first half: a {#id} attribute block on an English heading must appear
// unchanged on the Vietnamese one. doc/build-cover.md gets this right, and it
// is why its five table of contents links work: the headings are written
// `# Overview {#overview}` and `# Tổng quan {#overview}`, so `](#overview)`
// resolves on both sides.
//
// The second half is the one nobody thinks about. A heading with no explicit id
// is given one derived from its own text by the Markdown renderer, so
// "Should you constrain to pointer receivers?" and the Vietnamese for it are
// two different anchors. Every link to that section then has to be rewritten,
// which is what happened in blog/generic-interfaces.md, where the translator
// wrote `](#co-nen-rang-buoc-theo-pointer-receiver)` by hand. That is a guess
// at what the renderer will produce, it is not what the renderer produces, and
// even where the guess is right the anchor is now different from the English
// one, so every deep link into the Vietnamese page from outside it is a link
// that does not exist on the English page and vice versa.
//
// The fix is not to translate anchors better. It is to give the English heading
// an explicit id, upstream or in the fork, so that both languages carry the
// same one and the link is the same string in both files. So this rule refuses
// a same document anchor whose target heading has no explicit id, and the
// remedy it names is an edit to the English.
//
// The first version of that check found 38 anchors and 37 of them were the
// English's own. blog/pgo.md links to #example and blog/survey2020-results.md
// links to #TOC_6. in both languages, because the id is the renderer's and
// neither file writes it down. Subtracting the anchors the English does not
// resolve either leaves one finding on the whole corpus, and it is the real
// one: generic-interfaces.md guessing at #co-nen-rang-buoc-theo-pointer-receiver.
var ruleHeadingIDs = Rule{
	ID: "L05", Name: "heading ids", Severity: Refuse,
	Kinds: []content.Kind{content.KindMarkdown, content.KindHTML},
	Check: func(in Input) []Finding {
		var out []Finding
		en, vi := in.ENDoc.Headings, in.VIDoc.Headings
		if len(en) == len(vi) {
			for i := range en {
				// Only an English heading that declares an id has something the
				// Vietnamese can lose. Where the English declares none, both
				// sides get one from the renderer and neither file says what it
				// is, so there is nothing here to compare and a Vietnamese
				// heading that adds an explicit id is an improvement.
				if en[i].ID != "" && en[i].ID != vi[i].ID {
					out = append(out, Finding{Line: vi[i].Line, Msg: fmt.Sprintf(
						"heading %q carries id %q and the English carries %q",
						vi[i].Text, vi[i].ID, en[i].ID)})
				}
			}
		}
		// Anchors into this document, checked against the ids the document
		// actually declares.
		dangling := unresolved(in.VI, in.VIDoc)
		// An anchor the English does not resolve either is an anchor that
		// resolves through the renderer, which derives ids from heading text
		// that neither file writes down. blog/pgo.md links to #example and
		// blog/survey2020-results.md links to #TOC_6. on both sides. Reporting
		// those is reporting the English, so only the anchors the translation
		// introduced are left.
		for id := range unresolved(in.EN, in.ENDoc) {
			delete(dangling, id)
		}
		for _, id := range sortedKeys(dangling) {
			out = append(out, Finding{Line: dangling[id], Msg: fmt.Sprintf(
				"links to #%s and no heading in this file declares that id; "+
					"give the English heading an explicit {#%s} so both languages share it",
				id, id)})
		}
		return out
	},
}

// unresolved is the same document anchors a file links to and does not declare,
// each mapped to the line of the first link that uses it.
func unresolved(text string, doc content.Document) map[string]int {
	declared := map[string]bool{}
	for _, h := range doc.Headings {
		if h.ID != "" {
			declared[h.ID] = true
		}
	}
	for _, id := range explicitIDs(text) {
		declared[id] = true
	}
	out := map[string]int{}
	for _, l := range doc.Links {
		if !strings.HasPrefix(l.Target, "#") || l.Target == "#" {
			continue
		}
		id := strings.TrimPrefix(l.Target, "#")
		if declared[id] {
			continue
		}
		if _, ok := out[id]; !ok {
			out[id] = l.Line
		}
	}
	return out
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var explicitIDRE = regexp.MustCompile(`\{#([^\s}]+)[^}]*\}|\bid="([^"]+)"`)

func explicitIDs(text string) []string {
	var out []string
	for _, m := range explicitIDRE.FindAllStringSubmatch(text, -1) {
		if m[1] != "" {
			out = append(out, m[1])
		}
		if m[2] != "" {
			out = append(out, m[2])
		}
	}
	return out
}

// L06. Code was edited.
//
// The line this rule draws is the one that took the longest to get right. A
// first version demanded that fenced code blocks be identical and found 15
// files, and almost all of them were correct work: blog/unique.md translates
// the comments inside its Go, blog/wasmexport.md translates the comments in its
// wazero example, and doc/build-cover.md translates `// first run` in a shell
// transcript. Translating a comment is the whole point of translating a
// tutorial, and a rule that refuses it is a rule that gets turned off.
//
// So the comparison masks comments on both sides and compares what is left,
// which is the code. That leaves six files: gomod-ref.md, which has 24 fenced
// blocks against 27 and is the truncation again; generics.md, whose block 19
// lost lines; two blocks in fuzz.md where the replacement character came back
// as the HTML entity &#65533;; technical.md, which translated the words inside
// a fuzzing corpus listing; and type-inference.md, which translated the column
// headings of a table that happens to be inside a fence.
//
// The fence count and the info strings are compared unmasked, because a lost
// block and a `go` that became `đi` are both structure.
var ruleCode = Rule{
	ID: "L06", Name: "code", Severity: Refuse,
	Check: func(in Input) []Finding {
		en, vi := in.ENDoc.Fences, in.VIDoc.Fences
		if len(en) != len(vi) {
			return []Finding{{Msg: fmt.Sprintf(
				"has %d fenced code blocks against the English %d", len(vi), len(en))}}
		}
		var out []Finding
		for i := range en {
			if en[i].Info != vi[i].Info {
				out = append(out, Finding{Line: vi[i].Line, Msg: fmt.Sprintf(
					"code block %d is tagged %q and the English is tagged %q",
					i+1, vi[i].Info, en[i].Info)})
				continue
			}
			a := maskComments(en[i].Info, en[i].Body)
			b := maskComments(vi[i].Info, vi[i].Body)
			if a == b {
				continue
			}
			out = append(out, Finding{Line: vi[i].Line, Msg: fmt.Sprintf(
				"code block %d differs outside its comments: %s", i+1, firstDiff(a, b))})
		}
		return out
	},
}

var (
	// lineCommentRE matches // that does not follow a colon, so the // in
	// https:// is code and the // in `x := 1 // set x` is a comment.
	lineCommentRE  = regexp.MustCompile(`(^|[^:])//.*$`)
	hashCommentRE  = regexp.MustCompile(`(^|\s)#.*$`)
	blockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// hashLanguages are the fence tags where # opens a comment. Shell is in the
// list because doc/build-cover.md is a shell transcript with translated
// comments in it; YAML is because the solutions pages carry commented YAML.
var hashLanguages = map[string]bool{
	"sh": true, "bash": true, "shell": true, "console": true, "zsh": true,
	"yaml": true, "yml": true, "python": true, "py": true, "ruby": true,
	"dockerfile": true, "makefile": true, "make": true, "toml": true, "": true,
}

// slashLanguages are the fence tags where // opens a comment. The empty tag is
// in both maps: an untagged block is most often Go on this site, and masking
// too much only ever costs the rule a defect it would have caught, while
// masking too little costs it a false refusal on correct work.
var slashLanguages = map[string]bool{
	"go": true, "js": true, "javascript": true, "ts": true, "typescript": true,
	"c": true, "cpp": true, "c++": true, "java": true, "json5": true,
	"rust": true, "swift": true, "kotlin": true, "": true,
	// json is here for doc/security/vuln/database.md, which documents the
	// vulnerability database schema as JSON with // comments on every field.
	// It is not valid JSON and it is not meant to be, and the comments are the
	// documentation, so they are prose and they get translated.
	"json": true,
}

// maskComments replaces the text of every comment with a marker, so two blocks
// that differ only in their comments compare equal.
func maskComments(info, body string) string {
	info = strings.ToLower(strings.TrimSpace(info))
	var out []string
	if slashLanguages[info] {
		body = blockCommentRE.ReplaceAllString(body, "/*·*/")
	}
	for line := range strings.SplitSeq(body, "\n") {
		if slashLanguages[info] {
			line = lineCommentRE.ReplaceAllString(line, "$1//·")
		}
		if hashLanguages[info] {
			line = hashCommentRE.ReplaceAllString(line, "$1#·")
		}
		out = append(out, strings.TrimRight(line, " \t"))
	}
	return strings.Join(out, "\n")
}

// firstDiff names the first line that differs, because "differs" on a hundred
// line block is not something anybody can act on.
func firstDiff(a, b string) string {
	as, bs := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range max(len(as), len(bs)) {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if x != y {
			return fmt.Sprintf("line %d is %q and the English is %q",
				i+1, clip(y, 60), clip(x, 60))
		}
	}
	return "lengths differ"
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// L07. A link was dropped, or its target was rewritten.
//
// The link text and the optional title after the target are prose and change.
// ref/mod.md is the proof: it carries 450 links and five of them differ, and
// all five differ only in the title, `[...](/doc/mvs/upgrade.svg "MVS upgrade")`
// against `"Nâng cấp MVS"`, which is correct. A rule comparing whole links
// would refuse that file.
//
// The target is not prose. blog/json.md lost `/doc/tutorial/json` and
// blog/gofix.md lost `https://pkg.go.dev/cmd/go#hdr-Build_constraints`, in both
// cases because the sentence carrying the link was rewritten into one without
// it. A dropped link is invisible to a reader, who cannot miss what is not
// there, and after L03 it is the most common defect this corpus has: 34 links
// gone from 17 files, 9 of them out of contributors-summit-2019.md alone.
//
// Same document anchors are exempt here and handled by L05, which knows what
// the file declares.
var ruleLinks = Rule{
	ID: "L07", Name: "links", Severity: Refuse,
	Check: func(in Input) []Finding {
		en := targets(in.ENDoc.Links)
		vi := targets(in.VIDoc.Links)
		viSet := map[string]int{}
		for _, t := range vi {
			viSet[t]++
		}
		var out []Finding
		for _, t := range en {
			if viSet[t] > 0 {
				viSet[t]--
				continue
			}
			out = append(out, Finding{Msg: fmt.Sprintf(
				"drops the link to %s, which the English has", t)})
		}
		return out
	},
}

// targets is the link targets that must survive, which is every one that is not
// a same document anchor.
func targets(links []content.Link) []string {
	var out []string
	for _, l := range links {
		if strings.HasPrefix(l.Target, "#") {
			continue
		}
		out = append(out, l.Target)
	}
	return out
}

// L08. A template action was rewritten.
//
// _content_vi/site.tmpl carries 285 template actions and the Vietnamese
// differs from the English in six of them, every one of the form
// `{{- $alt := "Go gophers with wrench"}}` against `"Go gopher cầm cờ lê"`.
// That is alt text on an image. It is prose, it should be Vietnamese, and a
// rule demanding that actions be identical would refuse the site's own layout.
//
// What must not change is the action's skeleton: the function names, the
// variable names, the pipes, the operators. `{{projects` becoming `{{dự án`
// is not a bad translation, it is a template that does not parse and a site
// that does not build. So the comparison strips every quoted string and every
// backquoted block from both sides and compares what is left.
//
// That is a stronger rule than it looks, because the solutions pages carry
// their whole content inside a backquoted YAML argument to `{{projects}}` and
// `{{libraries}}`, so stripping the backquotes exempts exactly the prose and
// keeps the call.
//
// One finding on the corpus, and it is the exact failure the rule was written
// for: talks/2016/state-of-go.slide translated `{{- and -}}` into `{{- và -}}`.
// `and` is a template function. `và` is not, and the file does not render.
var ruleActions = Rule{
	ID: "L08", Name: "actions", Severity: Refuse,
	Check: func(in Input) []Finding {
		en, vi := in.ENDoc.Actions, in.VIDoc.Actions
		if len(en) != len(vi) {
			return []Finding{{Msg: fmt.Sprintf(
				"has %d template actions against the English %d", len(vi), len(en))}}
		}
		var out []Finding
		for i := range en {
			a, b := skeleton(en[i]), skeleton(vi[i])
			if a == b {
				continue
			}
			out = append(out, Finding{Msg: fmt.Sprintf(
				"template action %d is %s and the English is %s", i+1, clip(b, 80), clip(a, 80))})
		}
		return out
	},
}

var (
	quotedRE = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	backRE   = regexp.MustCompile("(?s)`[^`]*`")
	spaceRE  = regexp.MustCompile(`\s+`)
)

// skeleton is a template action with its literals removed, which is the part a
// translation must not touch.
func skeleton(action string) string {
	action = backRE.ReplaceAllString(action, "``")
	action = quotedRE.ReplaceAllString(action, `""`)
	return strings.TrimSpace(spaceRE.ReplaceAllString(action, " "))
}

// L09. The front matter changed shape.
//
// The keys must be the same set in the same order, and the values of
// VerbatimKeys must be identical.
//
// 139 findings on the corpus and 138 of them are one mistake made 138 times: a
// `template: true` added to a Vietnamese file whose English has no `template`
// key at all. That flag makes the site run the page body through text/template
// before rendering it, which is a different pipeline from the one the English
// page goes through, and on a page carrying {{ in a code block it is a build
// failure rather than a rendering difference. The 139th is solutions/grail.md,
// which has the right keys in the wrong order.
//
// The order matters and not only the set, because front matter is read as YAML
// and a reordering is the fingerprint of a file that was regenerated rather
// than edited. It costs nothing to keep and it is worth knowing.
var ruleFrontMatter = Rule{
	ID: "L09", Name: "front matter", Severity: Refuse,
	Kinds: []content.Kind{content.KindMarkdown, content.KindHTML},
	Check: func(in Input) []Finding {
		en := content.FrontMatterKeys(in.ENDoc.FrontMatter)
		vi := content.FrontMatterKeys(in.VIDoc.FrontMatter)
		var out []Finding
		if strings.Join(en, ",") != strings.Join(vi, ",") {
			out = append(out, Finding{Msg: fmt.Sprintf(
				"front matter keys are [%s] and the English has [%s]",
				strings.Join(vi, " "), strings.Join(en, " "))})
		}
		for _, key := range content.VerbatimKeys {
			a, okA := content.FrontMatterValue(in.ENDoc.FrontMatter, key)
			b, okB := content.FrontMatterValue(in.VIDoc.FrontMatter, key)
			if !okA || !okB || a == b {
				continue
			}
			out = append(out, Finding{Msg: fmt.Sprintf(
				"front matter %s is %q and the English is %q, and %s is copied not translated",
				key, clip(b, 60), clip(a, 60), key)})
		}
		return out
	},
}

// L10. A glossary term came back in English.
//
// GLOSSARY.md in godev-vn is the terminology the whole site is held to, and it
// exists because a page that renders "garbage collector" three ways reads as
// three pages. The rule reports a term whose English form is still standing in
// the Vietnamese prose, and it looks only at prose: code, links, template
// actions and inline code are stripped first, because `garbage collector` in a
// function name is a function name.
//
// Terms whose agreed rendering is the English word itself are skipped. The
// glossary keeps `commit`, `contributor`, `repository`, `workspace`,
// `generics` and `dependency` untranslated on purpose, and reporting those
// would be reporting every correct page on the site.
var ruleTerminology = Rule{
	ID: "L10", Name: "terminology", Severity: Notice,
	Check: func(in Input) []Finding {
		if in.Glossary == nil {
			return nil
		}
		source, answer := Prose(in.EN), Prose(in.VI)
		var out []Finding
		for _, t := range in.Glossary.Mentioned(source) {
			if t.KeepsEnglish() {
				continue
			}
			if !containsWord(answer, t.EN) {
				continue
			}
			out = append(out, Finding{Msg: fmt.Sprintf(
				"leaves %q in English, and the glossary writes it %q", t.EN, t.VI)})
		}
		return out
	},
}

// L11. The Vietnamese is not Vietnamese.
//
// A file whose prose carries no tone marks at all is a file that was not
// translated, whatever else is true of it. Vietnamese without diacritics is not
// Vietnamese; the language has six tones and five of them are written.
//
// The rule mostly overlaps L02, because a file with no tone marks in it is
// usually a byte for byte copy, and L02 gets there first with a notice. What is
// left is the interesting case: a file edited enough not to be a copy and never
// actually translated. There is one on the corpus,
// talks/2012/insidepresent/wire.html, which carries 0 Vietnamese letters in 275
// characters of prose.
//
// The threshold is a proportion rather than a count, because a page that is
// almost all code legitimately has three Vietnamese words in it. One tone
// marked rune per two hundred characters of prose. Below that the file is
// English with the front matter changed.
const MinDiacriticRatio = 1.0 / 200.0

var ruleLanguage = Rule{
	ID: "L11", Name: "language", Severity: Refuse,
	Check: func(in Input) []Finding {
		prose := Prose(in.VI)
		if len([]rune(prose)) < 200 {
			return nil
		}
		marked := 0
		for _, r := range prose {
			if isVietnamese(r) {
				marked++
			}
		}
		ratio := float64(marked) / float64(len([]rune(prose)))
		if ratio >= MinDiacriticRatio {
			return nil
		}
		return []Finding{{Msg: fmt.Sprintf(
			"carries %d Vietnamese letters in %d characters of prose, so it is not translated",
			marked, len([]rune(prose)))}}
	},
}

// isVietnamese reports whether a rune is one Vietnamese writes and English does
// not: the tone marked vowels, and đ.
func isVietnamese(r rune) bool {
	if r == 'đ' || r == 'Đ' {
		return true
	}
	if r < unicode.MaxASCII {
		return false
	}
	return unicode.Is(unicode.Latin, r) && unicode.IsLetter(r)
}

// commentaryRE is the shape of an answer that came back with a covering note on
// it. The model is told to write the translation and nothing else, and it
// mostly does, and when it does not the note is always one of these.
var commentaryRE = regexp.MustCompile(`(?im)^\s*(here is|here's|below is|sure[,!]|đây là bản dịch|bản dịch:|translation:|note:|ghi chú của người dịch)`)

// L12. The answer came back with commentary on it.
//
// Nothing on the corpus today, because the corpus was translated by hand and by
// an agent that was watched. It is here for the run: an unattended pipeline
// writing 480 files will produce this, and a "Here is the Vietnamese
// translation:" at the top of doc/install.html is a page that renders it.
//
// The wrapping fence is the other half. A model asked for Markdown often
// returns the whole answer inside ```markdown, which turns a page into a code
// listing of itself.
var ruleCommentary = Rule{
	ID: "L12", Name: "commentary", Severity: Refuse,
	Check: func(in Input) []Finding {
		body := in.VIDoc.Body
		var out []Finding
		if m := commentaryRE.FindString(body); m != "" && len(in.ENDoc.Body) > 0 &&
			!commentaryRE.MatchString(in.ENDoc.Body) {
			out = append(out, Finding{Msg: fmt.Sprintf(
				"opens with commentary, %q, which the English does not", strings.TrimSpace(m))})
		}
		trimmed := strings.TrimSpace(body)
		if strings.HasPrefix(trimmed, "```") && strings.HasSuffix(trimmed, "```") &&
			len(in.VIDoc.Fences) == 1 && len(in.ENDoc.Fences) != 1 {
			out = append(out, Finding{Msg: "is wrapped whole in a fenced code block"})
		}
		return out
	},
}

// L13. The English moved after the translation was made.
//
// This is the rule the upstream sync exists for. Merging 223 commits from
// golang/website changed 32 files under _content and added 9, and nothing
// under _content_vi moved, so 41 Vietnamese pages now describe a version of
// go.dev that is not the one shipping. Without a record of what each
// translation was made from there is no way to tell those 41 from the 400 that
// are current, and the only honest option is to re-translate everything.
//
// So every translation records the SHA-256 of the English it was made from, in
// translations.json at the root of the site repo, and this rule compares that
// against the English on disk. A file with no record at all is reported once
// and not refused, because the corpus predates the manifest and reporting 654
// refusals on the first run helps nobody. That is what the whole corpus looks
// like today, and the number goes down by one every time a file is translated
// through this tool.
var ruleStale = Rule{
	ID: "L13", Name: "stale", Severity: Refuse,
	Check: func(in Input) []Finding {
		if in.Manifest == nil {
			return nil
		}
		record, ok := in.Manifest.Get(in.Pair.Rel)
		if !ok {
			return []Finding{{Severity: Notice, Msg: "has no record of the English it was made from"}}
		}
		now := content.SHA256(in.EN)
		if record.EnglishSHA256 == now {
			return nil
		}
		return []Finding{{Msg: fmt.Sprintf(
			"was translated from English %s and the English on disk is %s, so it is out of date",
			short(record.EnglishSHA256), short(now))}}
	},
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}
