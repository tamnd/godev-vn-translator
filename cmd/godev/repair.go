package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/tamnd/godev-vn-translator/glossary"
	"github.com/tamnd/godev-vn-translator/quality"
	"github.com/tamnd/godev-vn-translator/translate"
)

const repairUsage = `usage: godev repair [flags] [path...]

Apply the transport repairs to translations already on disk.

A repair added today does nothing for the pages that came back before it
existed. This runs the same repairs over what is already under _content_vi,
proved by the English the same way, and writes back the files that come out
better. It asks no route anything.

With no paths it takes the whole corpus. With paths it takes those files, where
a path is under _content: ref/mod.md, or a directory like blog/.

flags:
  -n               say what would change and write nothing
  -group NAME      one section of the site: blog, doc, ref, tour, talks, wiki

A file is only written when the audit of the repaired text refuses no more than
the audit of what is there now, so a repair that would trade one defect for
another is reported and skipped rather than written.
`

// runRepair applies translate.Unmangle to the translations on disk.
//
// The 202 self linked urls under _content_vi are what this was written for. The
// repair that undoes them landed after those pages were translated, and the
// alternative to this command is asking for all nine files again, two of which
// are release notes running to several thousand lines. The answer is already
// determined by the English, so there is nothing to ask.
//
// It writes nothing it cannot show is an improvement. Every file is audited
// before and after and the write only happens when the refusals did not go up,
// which is what makes running this over the whole corpus a safe thing to do
// rather than a thing to be careful with.
//
// The manifest is deliberately not touched. A repaired file was still made from
// the English it says it was made from, and a repair is not a new translation.
func runRepair(checkout string, args []string) error {
	fs := flag.NewFlagSet("repair", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, repairUsage) }
	dry := fs.Bool("n", false, "say what would change and write nothing")
	group := fs.String("group", "", "one section of the site")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pairs, err := selectPairs(checkout, fs.Args(), false, *group)
	if err != nil {
		return err
	}
	terms, err := glossary.Load(checkout)
	if err != nil {
		terms = nil
	}
	manifest, err := quality.LoadManifest(checkout)
	if err != nil {
		return err
	}

	written, skipped, fixed := 0, 0, 0
	for _, pair := range pairs {
		if !pair.Exists {
			continue
		}
		en, err := pair.English()
		if err != nil {
			return err
		}
		vi, err := pair.Vietnamese()
		if err != nil {
			return err
		}
		repaired := translate.Unmangle(vi, en)
		if repaired == vi {
			continue
		}

		audit := func(text string) []quality.Finding {
			return quality.Audit(quality.Input{
				Pair: pair, EN: en, VI: text, Glossary: terms, Manifest: manifest,
			})
		}
		before, after := refusals(audit(vi)), refusals(audit(repaired))
		if after > before {
			skipped++
			fmt.Fprintf(os.Stderr, "%s  skipped, the repair refuses %d where what is there refuses %d\n",
				pair.Rel, after, before)
			continue
		}

		lines := changedLines(vi, repaired)
		fixed += before - after
		written++
		fmt.Fprintf(os.Stderr, "%s  %d lines, %d refusals gone\n", pair.Rel, lines, before-after)
		if *dry {
			continue
		}
		if err := os.WriteFile(pair.VietnamesePath, []byte(repaired), 0o644); err != nil {
			return err
		}
	}

	verb := "repaired"
	if *dry {
		verb = "would be repaired"
	}
	fmt.Fprintf(os.Stderr, "\n%d files %s, %d refusals gone\n", written, verb, fixed)
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "%d files left alone because the repair was not an improvement\n", skipped)
	}
	return nil
}

func refusals(findings []quality.Finding) int {
	n := 0
	for _, f := range findings {
		if f.Severity == quality.Refuse {
			n++
		}
	}
	return n
}

// changedLines counts the lines that differ, for the report. It is a count and
// not a diff, because a diff of a repaired release note is several hundred
// lines of url and the number is the part anybody reads.
func changedLines(was, now string) int {
	a, b := strings.Split(was, "\n"), strings.Split(now, "\n")
	if len(a) != len(b) {
		return max(len(a), len(b))
	}
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}
