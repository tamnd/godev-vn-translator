// Command godev translates go.dev into Vietnamese and refuses most of what
// comes back.
//
// It works against a checkout of tamnd/godev-vn, which is the fork of
// golang/website carrying _content in English and _content_vi in Vietnamese.
// Point it at that checkout with -C or with GODEV_VN, and every subcommand
// reads and writes files there.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const usage = `godev translates go.dev into Vietnamese.

usage: godev [-C dir] <command> [flags]

commands:
  audit      run the quality gates over the checkout and report
  routes     list the model routes in the order they would be tried
  doctor     probe every route and say which ones are answering
  queue      look at the work list on disk, reap it, retry it, drain it

The checkout defaults to $GODEV_VN, then to ../godev-vn beside this repo.
`

func main() {
	log := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
	root := flag.String("C", "", "the godev-vn checkout to work in")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	// Ctrl-C during a probe should stop the probe and print what came back, not
	// leave three ssh tunnels holding a request each.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, rest := args[0], args[1:]
	var runErr error
	switch cmd {
	case "audit", "queue":
		// Only the commands that work against the site need the site. Asking
		// for a checkout before printing a route table would be a strange thing
		// to fail on. The queue needs it because a work list is state about one
		// checkout and two checkouts must not share one.
		dir, err := checkout(*root)
		if err != nil {
			log("%v", err)
			os.Exit(1)
		}
		if cmd == "audit" {
			runErr = runAudit(dir, rest)
		} else {
			runErr = runQueue(dir, rest)
		}
	case "routes":
		runErr = runRoutes(rest)
	case "doctor":
		runErr = runDoctor(ctx, rest)
	case "help", "-h", "--help":
		fmt.Fprint(os.Stderr, usage)
		return
	default:
		log("unknown command %q", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if runErr != nil {
		log("%v", runErr)
		os.Exit(1)
	}
}

// checkout finds the site repo, and says what it tried when it cannot.
func checkout(flagValue string) (string, error) {
	candidates := []string{flagValue, os.Getenv("GODEV_VN")}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "..", "godev-vn"), wd)
	}
	var tried []string
	for _, c := range candidates {
		if strings.TrimSpace(c) == "" {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if info, err := os.Stat(filepath.Join(abs, "_content")); err == nil && info.IsDir() {
			return abs, nil
		}
		tried = append(tried, abs)
	}
	return "", fmt.Errorf("no godev-vn checkout found; set -C or GODEV_VN. Tried: %s",
		strings.Join(tried, ", "))
}
