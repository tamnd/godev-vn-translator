// Package glossary is the terminology the whole site is held to.
//
// It is read from GLOSSARY.md in the site repo rather than kept here, for the
// same reason the manifest lives there: it is a fact about that content, it is
// edited by whoever is translating, and a term added in a pull request against
// the site should take effect without a release of this tool.
//
// The file is Markdown with a table in it, which is a slightly awkward thing to
// parse and the right format anyway. A glossary that is only machine readable
// stops being maintained, and the whole value of this one is that a person
// opens it, disagrees with a row, and changes it. The table already exists with
// some sixty rows in it and it is the file everyone doing this work already
// looks at.
package glossary

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// File is the glossary in the site repo.
const File = "GLOSSARY.md"

// Term is one agreed rendering.
type Term struct {
	EN string
	VI string
	// Note is the third column, which says why. It is not used by any rule and
	// it is the most valuable column in the file.
	Note string
}

// KeepsEnglish reports whether the agreed Vietnamese for this term is the
// English word.
//
// The glossary deliberately keeps commit, contributor, repository, workspace,
// generics and dependency untranslated, on the reasoning that a Go programmer
// reading Vietnamese says "commit". A rule that reported those would report
// every correct page on the site, so this is what L10 filters on.
//
// The comparison is loose because the file writes some of them with a
// qualifier: "dependency" is written "dependency" and "a fortiori" style rows
// carry the English inside a longer phrase. A rendering that contains the
// English word is a rendering that keeps it.
func (t Term) KeepsEnglish() bool {
	en := strings.ToLower(strings.TrimSpace(t.EN))
	vi := strings.ToLower(strings.TrimSpace(t.VI))
	return en != "" && strings.Contains(vi, en)
}

// Glossary is the whole table.
type Glossary struct {
	Terms []Term
	// Path is where it was read from, so a report can cite it.
	Path string
}

// rowRE matches a Markdown table row with at least two cells.
var rowRE = regexp.MustCompile(`^\s*\|(.+)\|\s*$`)

// Load reads the glossary from a checkout. A checkout without one gets nil and
// no error: the audit runs with L10 off rather than not at all.
func Load(root string) (*Glossary, error) {
	path := filepath.Join(root, File)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g := Parse(string(raw))
	g.Path = path
	return g, nil
}

// Parse reads every table row in the document.
//
// Every table, not the one under a particular heading. The file has one table
// today and it will grow a second the first time somebody wants per-section
// terms, and a parser keyed to a heading would silently ignore it.
func Parse(text string) *Glossary {
	var g Glossary
	seen := map[string]bool{}
	for line := range strings.SplitSeq(text, "\n") {
		m := rowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cells := strings.Split(m[1], "|")
		if len(cells) < 2 {
			continue
		}
		en := strings.TrimSpace(cells[0])
		vi := strings.TrimSpace(cells[1])
		note := ""
		if len(cells) > 2 {
			note = strings.TrimSpace(cells[2])
		}
		// The header row and the --- separator under it are rows too.
		if en == "" || vi == "" || strings.Trim(en, "-: ") == "" {
			continue
		}
		if strings.EqualFold(en, "Thuật ngữ gốc") || strings.EqualFold(en, "Term") ||
			strings.EqualFold(en, "English") {
			continue
		}
		key := strings.ToLower(en)
		if seen[key] {
			continue
		}
		seen[key] = true
		g.Terms = append(g.Terms, Term{EN: en, VI: vi, Note: note})
	}
	// Longest first, so that a text carrying "garbage collector" is matched by
	// that row and not by a shorter "collector" row that happens to be above it.
	sort.SliceStable(g.Terms, func(i, j int) bool {
		return len(g.Terms[i].EN) > len(g.Terms[j].EN)
	})
	return &g
}

// Mentioned returns the terms whose English appears in the given text, which
// is what makes L10 a check on the terms this page actually uses rather than on
// the whole table.
func (g *Glossary) Mentioned(text string) []Term {
	if g == nil {
		return nil
	}
	lower := strings.ToLower(text)
	var out []Term
	for _, t := range g.Terms {
		if strings.Contains(lower, strings.ToLower(t.EN)) {
			out = append(out, t)
		}
	}
	return out
}

// Find returns the agreed rendering of one term.
func (g *Glossary) Find(en string) (Term, bool) {
	if g == nil {
		return Term{}, false
	}
	for _, t := range g.Terms {
		if strings.EqualFold(t.EN, en) {
			return t, true
		}
	}
	return Term{}, false
}

// Prompt renders the glossary as the block that goes in a translation request.
//
// One term per line, English then the Vietnamese, and nothing else. The notes
// are left out: they are addressed to a person deciding what the rendering
// should be, the model is being told what it is, and every character spent on
// the glossary is a character not spent on the passage.
func (g *Glossary) Prompt() string {
	if g == nil || len(g.Terms) == 0 {
		return ""
	}
	terms := append([]Term(nil), g.Terms...)
	sort.SliceStable(terms, func(i, j int) bool { return terms[i].EN < terms[j].EN })
	var out strings.Builder
	out.WriteString("Terminology. Use the Vietnamese on the right for the English on the left.\n\n")
	for _, t := range terms {
		out.WriteString(t.EN)
		out.WriteString("  ->  ")
		out.WriteString(t.VI)
		out.WriteString("\n")
	}
	return out.String()
}

// Relevant is the glossary cut down to the terms one passage uses.
//
// The whole table is some sixty rows now and will be several hundred when the
// site is properly covered. Sending all of it with every chunk is thousands of
// characters of prompt per call for terms the passage does not contain, which
// costs context on the long pages that need it most.
func (g *Glossary) Relevant(text string) *Glossary {
	if g == nil {
		return nil
	}
	return &Glossary{Terms: g.Mentioned(text), Path: g.Path}
}
