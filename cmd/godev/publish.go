package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/tamnd/godev-vn-translator/publish"
	"github.com/tamnd/godev-vn-translator/quality"
	"github.com/tamnd/godev-vn-translator/site"
)

// runPublish exports the site to a directory a static host can serve.
//
// It refuses to export a checkout the audit refuses, on the same reasoning as
// every other gate here: a bad translation that is only in the repository is
// work in progress, and a bad translation that is on godev-vn.tamnd.com is a
// bad translation of go.dev on the internet. The pin is the same -max the audit
// takes, because the corpus is being worked down and a publish that demanded
// zero would never run.
func runPublish(ctx context.Context, root string, args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	out := fs.String("out", "dist", "write the export here, relative to the checkout")
	host := fs.String("host", "", "override the canonical host in SITE.md")
	addr := fs.String("addr", "127.0.0.1:8099", "run the site on this address during the export")
	max := fs.Int("max", -1, "allow up to this many refusals before refusing to publish")
	skipAudit := fs.Bool("no-audit", false, "export without running the gates first")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !*skipAudit {
		result, err := quality.Run(root)
		if err != nil {
			return err
		}
		slack, err := auditStatus(len(result.Refusals()), *max)
		if slack != "" {
			fmt.Fprintln(os.Stderr, slack)
		}
		if err != nil {
			return fmt.Errorf("not publishing: %w", err)
		}
	}

	// The addresses come from the checkout, not from this tool. Moving the site
	// to a new domain is then a pull request against the content and not a
	// release of the translator, which is the same arrangement GLOSSARY.md and
	// translations.json are under. The flag stays for a one-off export to a
	// preview host, and it overrides only which host is canonical: the other
	// names the deploy answers on are a fact about the deploy either way.
	conf, err := site.Load(root)
	if err != nil {
		return err
	}
	canonical := conf.Host()
	if strings.TrimSpace(*host) != "" {
		canonical = strings.TrimSpace(*host)
	}
	res, err := publish.Run(ctx, publish.Options{
		Root:        root,
		Out:         *out,
		Host:        canonical,
		Redirecting: conf.Redirecting(),
		Waiting:     conf.Waiting(),
		Mirrors:     conf.Mirrors(),
		Addr:        *addr,
		Log:         func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	if err != nil {
		return err
	}
	for _, s := range res.Skipped {
		fmt.Fprintf(os.Stderr, "did not export %s\n", s)
	}
	fmt.Printf("%d pages, %d assets, %d redirects, %.1f MB in %s\n",
		res.Pages, res.Assets, res.Redirects, float64(res.Bytes)/(1<<20), res.Out)
	return nil
}
