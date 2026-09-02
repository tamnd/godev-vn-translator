// Package quality is the audit. It compares a Vietnamese file with the English
// it was made from and reports every way the two differ that they were told not
// to.
//
// Every rule here was written against a defect that is on disk in
// tamnd/godev-vn today, not against a defect that seemed likely. The counts in
// each rule's comment are what the rule found on the corpus after the upstream
// sync, over 489 Markdown files, 78 HTML pages, 67 slides, 24 templates, 13
// YAML files and 9 articles, and they are the reason the rule exists. A rule
// with no defect behind it is a rule that will spend its life refusing correct
// work.
//
// The thing to understand about translating a documentation site, as opposed to
// prose, is that most of a page is not prose. A page of ref/mod.md is 450 links,
// 103 code blocks and four template actions wrapped in sentences, and all three
// of those have to come through a translation byte for byte while the sentences
// around them change completely. So almost every rule below is a sequence
// extracted from both sides and compared element by element, and the interesting
// work is in deciding which part of each element is prose and which is not.
//
// Getting that line wrong in either direction is expensive. Demand that a code
// block be identical and you refuse blog/unique.md, where translating the Go
// comments is exactly right. Demand nothing and you accept doc/build-cover.md
// with a translated shell command in it. The line is drawn per rule, in the
// rule's own comment, against a named file.
//
// What none of this can do is tell a correct translation from a fluent wrong
// one. Every rule is about the shape of the answer, and a paragraph that says
// the opposite of the English in good Vietnamese with the links intact passes
// all of them. That is what the round trip in translate/ and a human reader are
// for, and it is why nothing here claims a translation is right.
package quality

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/tamnd/godev-vn-translator/content"
	"github.com/tamnd/godev-vn-translator/glossary"
)

// Severity says what a finding costs.
//
// Two levels and not five. A gate that a run can ignore is a gate that a run
// does ignore, so the only question worth asking is whether the site ships with
// this in it.
type Severity string

const (
	// Refuse means the translation is not fit to publish. The chunk is asked
	// again during a run, and the build fails in CI.
	Refuse Severity = "refuse"
	// Notice means somebody should look, but the page is better with it than
	// without it. An untranslated file is a Notice: English on a Vietnamese
	// site is a gap, not a lie.
	Notice Severity = "notice"
)

// A Finding is one way a file falls short.
type Finding struct {
	// Rule is the identifier, L01 to L13. It is stable, it is what a report
	// groups by, and it is what a commit message cites.
	Rule string
	// Name is the rule in a word, because the number means nothing to somebody
	// reading a failure for the first time.
	Name     string
	Severity Severity
	// Path is the file under _content_vi, so a finding is something you can
	// open.
	Path string
	// Line is one based in the Vietnamese file, and 0 when the finding is
	// about the file as a whole.
	Line int
	Msg  string
}

func (f Finding) String() string {
	where := f.Path
	if f.Line > 0 {
		where = fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	return fmt.Sprintf("%s  %s  %s: %s", f.Rule, where, f.Name, f.Msg)
}

// Rule is one gate.
type Rule struct {
	ID       string
	Name     string
	Severity Severity
	// Kinds limits the rule to the file kinds it makes sense for. Empty is
	// every translatable kind.
	Kinds []content.Kind
	// Check compares one pair. It is given both texts already read, because a
	// gate that does its own IO cannot be tested without a directory.
	Check func(in Input) []Finding
}

// Input is what a rule is given.
type Input struct {
	Pair content.Pair
	// EN and VI are the two files. VI is the empty string when there is no
	// translation, which only L01 is expected to have an opinion about.
	EN, VI string
	// ENDoc and VIDoc are the parsed forms, computed once and shared, because
	// eleven of the thirteen rules want them and parsing 480 files thirteen
	// times over is thirteen times the work for the same answer.
	ENDoc, VIDoc content.Document
	// Glossary is the terminology the translation is held to. Nil disables L10
	// rather than failing, so a checkout with no GLOSSARY.md still audits.
	Glossary *glossary.Glossary
	// Manifest records what English each translation was made from. Nil
	// disables L13.
	Manifest *Manifest
}

// Rules is every gate, in the order a report lists them.
func Rules() []Rule {
	return []Rule{
		rulePresence,
		ruleUntranslated,
		ruleTruncation,
		ruleHeadings,
		ruleHeadingIDs,
		ruleCode,
		ruleLinks,
		ruleActions,
		ruleFrontMatter,
		ruleTerminology,
		ruleLanguage,
		ruleCommentary,
		ruleStale,
	}
}

func (r Rule) applies(kind content.Kind) bool {
	return len(r.Kinds) == 0 || slices.Contains(r.Kinds, kind)
}

// Audit runs every rule over one pair.
func Audit(in Input) []Finding {
	in.ENDoc = content.Parse(in.EN)
	in.VIDoc = content.Parse(in.VI)
	var out []Finding
	for _, rule := range Rules() {
		if !rule.applies(in.Pair.Kind) {
			continue
		}
		// A rule other than L01 has nothing to say about a file that is not
		// there. Running them anyway produces thirteen findings per missing
		// file, which buries the one that matters.
		if in.VI == "" && rule.ID != rulePresence.ID {
			continue
		}
		for _, f := range rule.Check(in) {
			f.Rule, f.Name = rule.ID, rule.Name
			// A rule may downgrade one of its own findings. L13 does it for the
			// file that has no record at all, which is a gap in the manifest
			// rather than a defect in the translation. So the rule's severity is
			// a default and not an override.
			if f.Severity == "" {
				f.Severity = rule.Severity
			}
			if f.Path == "" {
				f.Path = content.VietnameseDir + "/" + in.Pair.Rel
			}
			out = append(out, f)
		}
	}
	return out
}

// Report is the result of auditing a whole checkout.
type Report struct {
	Findings []Finding
	// Pairs is how many files were compared, so a report that found nothing
	// can say what it looked at.
	Pairs int
	// Translated is how many of them have a Vietnamese file that is not a copy
	// of the English.
	Translated int
}

// Refusals is the findings that stop a publish.
func (r Report) Refusals() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity == Refuse {
			out = append(out, f)
		}
	}
	return out
}

// ByRule counts the findings per rule.
func (r Report) ByRule() map[string]int {
	out := map[string]int{}
	for _, f := range r.Findings {
		out[f.Rule]++
	}
	return out
}

// Coverage is the fraction of translatable files that carry a real translation.
func (r Report) Coverage() float64 {
	if r.Pairs == 0 {
		return 0
	}
	return float64(r.Translated) / float64(r.Pairs)
}

// Table renders a report the way the CLI prints it: one line per rule, worst
// first, then the refusals in full.
func (r Report) Table() string {
	counts := r.ByRule()
	var out strings.Builder
	fmt.Fprintf(&out, "%d files, %d translated (%.1f%%)\n\n",
		r.Pairs, r.Translated, 100*r.Coverage())
	fmt.Fprintf(&out, "%-5s  %-14s  %-7s  %s\n", "rule", "name", "sev", "count")
	for _, rule := range Rules() {
		n := counts[rule.ID]
		if n == 0 {
			continue
		}
		fmt.Fprintf(&out, "%-5s  %-14s  %-7s  %d\n", rule.ID, rule.Name, rule.Severity, n)
	}
	refusals := r.Refusals()
	if len(refusals) == 0 {
		fmt.Fprintf(&out, "\nnothing refused\n")
		return out.String()
	}
	fmt.Fprintf(&out, "\n%d refused:\n", len(refusals))
	sort.SliceStable(refusals, func(i, j int) bool {
		if refusals[i].Rule != refusals[j].Rule {
			return refusals[i].Rule < refusals[j].Rule
		}
		return refusals[i].Path < refusals[j].Path
	})
	for _, f := range refusals {
		fmt.Fprintf(&out, "  %s\n", f)
	}
	return out.String()
}
