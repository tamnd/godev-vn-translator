package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tamnd/godev-vn-translator/quality"
)

// runAudit is the gate. It is what CI runs and what a translation run runs
// after every file it writes.
//
// The exit status is the whole interface: zero when nothing is refused, one
// when something is. Notices never fail the build, because there are hundreds
// of them and a gate that is always red is a gate that gets a --no-verify.
func runAudit(root string, args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	report := fs.String("report", "", "write the Markdown report to this file")
	rule := fs.String("rule", "", "show findings for one rule only, by id or name")
	all := fs.Bool("all", false, "print notices as well as refusals")
	quiet := fs.Bool("quiet", false, "print nothing, just set the exit status")
	// -max is the ratchet, and it exists because the site repo needs a gate it
	// can turn on today.
	//
	// The corpus has 227 refusals. A required check that demands zero is red on
	// the first pull request and every pull request after it, including the ones
	// fixing the refusals, and a check that is always red is a check somebody
	// turns off. So the site's CI pins the number it has and fails on 228.
	//
	// It only ever goes down. A pull request that fixes ten refusals lowers the
	// pin by ten in the same diff, which is a reviewable claim about what the
	// change did, and the day it reaches zero the flag comes off and the gate is
	// the plain one. Without a pin the default is unchanged: any refusal fails.
	max := fs.Int("max", -1, "allow up to this many refusals before failing, for a corpus being worked down")
	if err := fs.Parse(args); err != nil {
		return err
	}

	result, err := quality.Run(root)
	if err != nil {
		return err
	}

	if *report != "" {
		path := *report
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(result.Markdown()), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	}

	if !*quiet {
		switch {
		case *rule != "":
			printRule(result, *rule)
		case *all:
			for _, f := range result.Findings {
				fmt.Println(f)
			}
			fmt.Print("\n" + result.Table())
		default:
			fmt.Print(result.Table())
		}
	}

	slack, err := auditStatus(len(result.Refusals()), *max)
	if slack != "" {
		fmt.Fprintln(os.Stderr, slack)
	}
	return err
}

// auditStatus turns a refusal count and a pin into an exit status and, when the
// pin has room left in it, a line saying how much.
//
// Split out from runAudit because it is the part with the off by one in it. The
// pin is the number the checkout is allowed to have, so equal passes and one
// more fails, and that is easier to assert than to read.
func auditStatus(refusals, max int) (string, error) {
	switch {
	case max < 0:
		if refusals > 0 {
			return "", fmt.Errorf("%d findings refuse the translation", refusals)
		}
	case refusals > max:
		return "", fmt.Errorf("%d findings refuse the translation, which is %d more than the %d this checkout is pinned to",
			refusals, refusals-max, max)
	case refusals < max:
		// Worth saying out loud. The pin is a claim about the corpus, and one
		// that has drifted low is a claim nobody is maintaining, so the run
		// says by how much rather than passing quietly.
		return fmt.Sprintf("%d refusals against a pin of %d, so -max can come down by %d",
			refusals, max, max-refusals), nil
	}
	return "", nil
}

func printRule(result quality.Report, want string) {
	want = strings.ToLower(strings.TrimSpace(want))
	n := 0
	for _, f := range result.Findings {
		if strings.ToLower(f.Rule) != want && strings.ToLower(f.Name) != want {
			continue
		}
		fmt.Println(f)
		n++
	}
	fmt.Printf("\n%d findings for %s\n", n, want)
}
