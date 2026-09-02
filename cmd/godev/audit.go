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

	if n := len(result.Refusals()); n > 0 {
		return fmt.Errorf("%d findings refuse the translation", n)
	}
	return nil
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
