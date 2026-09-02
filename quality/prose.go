package quality

import (
	"regexp"
	"strings"
	"unicode"
)

// Prose is the part of a file that is meant to be read as a sentence.
//
// Everything the glossary and the language rule care about is prose, and almost
// nothing on this site is. A page is front matter, fenced code, inline code,
// link targets, template actions and HTML tags, with sentences in the gaps, and
// each of those categories produces false findings if it is left in.
//
// The order matters and is the order the defects appeared in. Fences go first
// because a Go example carrying the word "repository" in an import path is not
// a page that left a glossary term in English. Link targets go before link
// text, because /doc/tutorial/generics is a path and "generics" in it is not
// prose. Template actions go before HTML, because an action's argument is a
// backquoted YAML block that would otherwise be read as a sentence.
func Prose(text string) string {
	text = stripFrontMatter(text)
	text = stripFences(text)
	text = actionBlockRE.ReplaceAllString(text, " ")
	text = htmlCommentRE.ReplaceAllString(text, " ")
	text = styleRE.ReplaceAllString(text, " ")
	text = scriptRE.ReplaceAllString(text, " ")
	text = preRE.ReplaceAllString(text, " ")
	// present links go before Markdown ones. `[[url][text]]` is how a .slide and
	// an .article write a link, and the Markdown expression cannot see it because
	// there is no parenthesis in it. The corpus has 401 of them across 56 files,
	// so leaving them in put every one of those urls into the prose.
	text = presentLinkRE.ReplaceAllString(text, "$2")
	// A link keeps its text and loses its target. The title after the target is
	// prose and is kept, which is what makes ref/mod.md's "Nâng cấp MVS" count.
	text = linkTargetRE.ReplaceAllString(text, "$1 $2")
	text = imageRE.ReplaceAllString(text, " ")
	text = autolinkRE.ReplaceAllString(text, " ")
	text = inlineCodeRE.ReplaceAllString(text, " ")
	text = htmlTagRE.ReplaceAllString(text, " ")
	text = attrBlockRE.ReplaceAllString(text, " ")
	return strings.TrimSpace(spaceRE.ReplaceAllString(text, " "))
}

var (
	frontRE       = regexp.MustCompile(`(?s)\A---\r?\n.*?\r?\n---\r?\n`)
	actionBlockRE = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	htmlCommentRE = regexp.MustCompile(`(?s)<!--.*?-->`)
	// styleRE and scriptRE take the body and not just the tags. htmlTagRE below
	// removes <style> and </style> and leaves the CSS between them standing, and
	// CSS is letters in the Latin alphabet with no tone marks on any of them,
	// which is exactly what L11 is looking for when it decides a page was never
	// translated. `talks/2012/insidepresent/wire.html` is thirteen lines of CSS
	// and one empty div, it is byte for byte correct in both languages because
	// there is nothing in it to translate, and it was the only L11 refusal on the
	// corpus. Two expressions rather than one because RE2 has no backreference to
	// match the closing tag against the opening one.
	styleRE  = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
	scriptRE = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	// preRE is the third of the same shape, and it is the one a reader is most
	// likely to argue with, because a <pre> body is visible on the page. It is
	// still not prose. doc/contribute.html has "remote: error: author email
	// address" inside one, which is git talking, and it was being reported as a
	// page that left the glossary term "author" in English.
	preRE = regexp.MustCompile(`(?is)<pre\b[^>]*>.*?</pre\s*>`)
	// The label is optional because present says it is: `[[url]]` renders the url
	// as its own text. There is nothing to keep in that case, which is the same
	// answer autolinkRE gives for the Markdown spelling of it.
	presentLinkRE = regexp.MustCompile(`\[\[([^\[\]\s]*)\](?:\[([^\[\]]*)\])?\]`)
	linkTargetRE  = regexp.MustCompile(`\[([^\]]*)\]\([^\s)]*\s*(?:"([^"]*)")?\s*\)`)
	imageRE       = regexp.MustCompile(`!\[[^\]]*\]`)
	autolinkRE    = regexp.MustCompile(`<https?://[^>]*>|\bhttps?://\S+`)
	inlineCodeRE  = regexp.MustCompile("`[^`]*`")
	htmlTagRE     = regexp.MustCompile(`(?s)<[^>]+>`)
	attrBlockRE   = regexp.MustCompile(`\{[#.][^}]*\}`)
)

func stripFrontMatter(text string) string {
	if m := frontRE.FindString(text); m != "" {
		return text[len(m):]
	}
	return text
}

// fenceOpenRE has to match the opening of a fence and remember its marker, and
// Go's regexp has no backreference to match the closing one with. So the
// stripping is done a line at a time, which is what the parser in content/ does
// for the same reason.
var fenceOpenRE = regexp.MustCompile("^[ \t]*(```+|~~~+)")

func stripFences(text string) string {
	var out []string
	marker := ""
	for line := range strings.SplitSeq(text, "\n") {
		if marker == "" {
			if m := fenceOpenRE.FindStringSubmatch(line); m != nil {
				marker = m[1]
			} else {
				out = append(out, line)
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, marker) && strings.TrimRight(trimmed, string(marker[0])) == "" {
			marker = ""
		}
	}
	return strings.Join(out, "\n")
}

// wordSpans returns every rune range in the text where the term stands as a
// word rather than as a fragment of one.
//
// The distinction earns its keep on short glossary terms. "Go" is in the
// glossary and it is inside "Google", "Golang" and "algorithm"; a substring
// match would report every page on the site. The boundaries are checked in
// runes rather than with a word-boundary regexp because the text around a term
// is Vietnamese, and a tone marked vowel is a letter that \b does not know
// about.
//
// The ranges rather than a yes or no, because L10 has to tell an English word
// that belongs to the page from one that belongs to a longer phrase the
// glossary keeps in English. Indices are into the lowercased text.
func wordSpans(text, term string) [][2]int {
	text, term = strings.ToLower(text), strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return nil
	}
	runes := []rune(text)
	t := []rune(term)
	var out [][2]int
	for i := 0; i+len(t) <= len(runes); i++ {
		if string(runes[i:i+len(t)]) != term {
			continue
		}
		if !boundedLeft(runes, i) || !boundedRight(runes, i+len(t)) {
			continue
		}
		out = append(out, [2]int{i, i + len(t)})
	}
	return out
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// joins reports whether this character can hold two halves of one name
// together. A dot, a hyphen and a slash do; a comma, a quote and a parenthesis
// do not, because nobody puts those inside an identifier.
func joins(r rune) bool {
	return r == '.' || r == '-' || r == '/'
}

// boundedLeft and boundedRight say whether the term ends where it is, rather
// than being the middle of something longer.
//
// A letter next to it is the easy case and is what a word boundary means
// anywhere. The joiner is the case the corpus taught. `release.2010-10-27`,
// `release-branch.go1.4`, `http.Redirect` and `/wiki/#the-go-community` are all
// on the site, they are all a name and not a sentence, and every one of them was
// being reported as a page that left a glossary term in English. A joiner with
// more of a token on the other side of it means the term is a piece of a name.
//
// The joiner has to have something after it. A sentence ends in a full stop, and
// "một release." is a page that left the word in English no matter what follows.
// That is the difference between a dot that joins and a dot that terminates, and
// it is the whole reason this is two functions rather than a character class.
func boundedLeft(runes []rune, i int) bool {
	if i == 0 {
		return true
	}
	if isWordRune(runes[i-1]) {
		return false
	}
	return !(joins(runes[i-1]) && i >= 2 && isWordRune(runes[i-2]))
}

func boundedRight(runes []rune, j int) bool {
	if j == len(runes) {
		return true
	}
	if isWordRune(runes[j]) {
		return false
	}
	return !(joins(runes[j]) && j+1 < len(runes) && isWordRune(runes[j+1]))
}
