package quality

import (
	"fmt"
	"strings"

	"github.com/tamnd/godev-vn-translator/content"
	"github.com/tamnd/godev-vn-translator/glossary"
)

// Run audits a whole checkout of godev-vn.
func Run(root string) (Report, error) {
	pairs, err := content.Root(root).Pairs()
	if err != nil {
		return Report{}, err
	}
	g, err := glossary.Load(root)
	if err != nil {
		// A checkout with an unreadable glossary still audits, with L10 off.
		// Twelve rules working is better than none, and the CLI says so.
		g = nil
	}
	manifest, err := LoadManifest(root)
	if err != nil {
		return Report{}, fmt.Errorf("read %s: %w", ManifestFile, err)
	}

	report := Report{Pairs: len(pairs)}
	for _, pair := range pairs {
		en, err := pair.English()
		if err != nil {
			return Report{}, err
		}
		vi, err := pair.Vietnamese()
		if err != nil {
			return Report{}, err
		}
		if pair.Exists && vi != en {
			report.Translated++
		}
		report.Findings = append(report.Findings, Audit(Input{
			Pair: pair, EN: en, VI: vi, Glossary: g, Manifest: manifest,
		})...)
	}
	return report, nil
}

// Markdown renders the report as the file committed to reports/audit.md.
//
// A table of counts and then the refusals, because the counts are what a
// milestone is measured against and the refusals are what somebody actually
// works from. The notices are left out of the file deliberately: there are
// hundreds of them, they change on every upstream sync, and a report nobody
// reads to the end is a report that hides its own conclusion.
func (r Report) Markdown() string {
	var out strings.Builder
	counts := r.ByRule()
	fmt.Fprintf(&out, "# Translation audit\n\n")
	fmt.Fprintf(&out, "%d translatable files, %d carry a real translation, %.1f%% coverage.\n\n",
		r.Pairs, r.Translated, 100*r.Coverage())
	fmt.Fprintf(&out, "| rule | name | severity | findings |\n|---|---|---|---|\n")
	for _, rule := range Rules() {
		fmt.Fprintf(&out, "| %s | %s | %s | %d |\n",
			rule.ID, rule.Name, rule.Severity, counts[rule.ID])
	}
	refusals := r.Refusals()
	fmt.Fprintf(&out, "\n## Refusals (%d)\n\n", len(refusals))
	if len(refusals) == 0 {
		fmt.Fprintf(&out, "None. The site is fit to publish.\n")
		return out.String()
	}
	for _, rule := range Rules() {
		var mine []Finding
		for _, f := range refusals {
			if f.Rule == rule.ID {
				mine = append(mine, f)
			}
		}
		if len(mine) == 0 {
			continue
		}
		fmt.Fprintf(&out, "### %s %s (%d)\n\n", rule.ID, rule.Name, len(mine))
		for _, f := range mine {
			where := f.Path
			if f.Line > 0 {
				where = fmt.Sprintf("%s:%d", f.Path, f.Line)
			}
			fmt.Fprintf(&out, "- `%s` %s\n", where, f.Msg)
		}
		fmt.Fprintln(&out)
	}
	return out.String()
}
