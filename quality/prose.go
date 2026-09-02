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
	styleRE      = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
	scriptRE     = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	linkTargetRE = regexp.MustCompile(`\[([^\]]*)\]\([^\s)]*\s*(?:"([^"]*)")?\s*\)`)
	imageRE      = regexp.MustCompile(`!\[[^\]]*\]`)
	autolinkRE   = regexp.MustCompile(`<https?://[^>]*>|\bhttps?://\S+`)
	inlineCodeRE = regexp.MustCompile("`[^`]*`")
	htmlTagRE    = regexp.MustCompile(`(?s)<[^>]+>`)
	attrBlockRE  = regexp.MustCompile(`\{[#.][^}]*\}`)
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

// containsWord reports whether the term appears in the text as a word rather
// than as a fragment of one.
//
// The distinction earns its keep on short glossary terms. "Go" is in the
// glossary and it is inside "Google", "Golang" and "algorithm"; a substring
// match would report every page on the site. The boundaries are checked in
// runes rather than with a word-boundary regexp because the text around a term
// is Vietnamese, and a tone marked vowel is a letter that \b does not know
// about.
func containsWord(text, term string) bool {
	text, term = strings.ToLower(text), strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return false
	}
	runes := []rune(text)
	t := []rune(term)
	for i := 0; i+len(t) <= len(runes); i++ {
		if string(runes[i:i+len(t)]) != term {
			continue
		}
		if i > 0 && isWordRune(runes[i-1]) {
			continue
		}
		if i+len(t) < len(runes) && isWordRune(runes[i+len(t)]) {
			continue
		}
		return true
	}
	return false
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}
