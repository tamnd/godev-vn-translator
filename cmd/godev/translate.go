package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tamnd/godev-vn-translator/content"
	"github.com/tamnd/godev-vn-translator/glossary"
	"github.com/tamnd/godev-vn-translator/quality"
	"github.com/tamnd/godev-vn-translator/queue"
	"github.com/tamnd/godev-vn-translator/route"
	"github.com/tamnd/godev-vn-translator/translate"
)

const translateUsage = `usage: godev translate [flags] [path...]

Translate the pages that need it and write the ones that come back clean.

With no paths it takes the whole corpus, which is what a full run is. With paths
it takes those files and the pieces of them that are not done, where a path is
under _content: ref/mod.md, or a directory like blog/, or a section like doc.

flags:
  -gap             only the files with no translation or a stale one, which is
                   the 41 file sync gap and is the work that matters first
  -group NAME      one section of the site: blog, doc, ref, tour, talks, wiki
  -workers N       calls in flight, default the fleet's own lane count
  -budget N        bytes per piece, default 6000
  -plan            say what would be asked, queue nothing, and stop
  -assemble        put finished pages together and stop, asking nothing
  -force           ask again for pieces that are already done
  -root PATH       queue directory, default the one godev queue uses

Route flags are the same as godev doctor: -routes, -route, -key, -timeout.

A run is interruptible. The answers are on disk, so stopping and starting again
carries on rather than starting over, and a page is written only when every
piece of it is back and the whole file passes the audit.
`

func runTranslate(ctx context.Context, checkout string, args []string) error {
	fs := flag.NewFlagSet("translate", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, translateUsage) }
	flags := addRouteFlags(fs)
	gap := fs.Bool("gap", false, "only the files with no translation or a stale one")
	group := fs.String("group", "", "one section of the site")
	workers := fs.Int("workers", 0, "calls in flight, default the fleet's lane count")
	budget := fs.Int("budget", 0, "bytes per piece")
	planOnly := fs.Bool("plan", false, "say what would be asked, queue nothing, and stop")
	assembleOnly := fs.Bool("assemble", false, "put finished pages together and stop")
	force := fs.Bool("force", false, "ask again for pieces that are already done")
	root := fs.String("root", queueRoot(checkout), "queue directory")
	expected := fs.Duration("expected", 0, "how long one piece is assumed to take")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pairs, err := selectPairs(checkout, fs.Args(), *gap, *group)
	if err != nil {
		return err
	}
	if len(pairs) == 0 {
		fmt.Fprintln(os.Stderr, "nothing to translate")
		return nil
	}

	q, err := queue.Open(*root)
	if err != nil {
		return err
	}
	terms, err := glossary.Load(checkout)
	if err != nil {
		return err
	}

	engine := &translate.Engine{
		Root:     content.Root(checkout),
		Work:     translate.Store{Root: filepath.Dir(*root)},
		Queue:    q,
		Glossary: terms,
		Budget:   *budget,
		Expected: *expected,
		Log: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		},
	}

	if *assembleOnly {
		return report(engine.Assemble(pairs))
	}

	plan, err := engine.Plan(pairs, *planOnly)
	if err != nil {
		return err
	}
	verb := "added to the queue"
	if *planOnly {
		verb = "that would be added to the queue"
	}
	fmt.Fprintf(os.Stderr, "%d files, %d pieces to ask about, %d copied through, %d %s\n",
		plan.Files, plan.Asked, plan.Copied, plan.Added, verb)
	if *planOnly {
		// -force is left alone here for the same reason the plan inserts
		// nothing. Retry moves finished jobs back to pending, and a run that
		// says it will stop and then rearranges the queue is a run that costs
		// fleet time the next time somebody starts one.
		if *force {
			fmt.Fprintln(os.Stderr, "-force does nothing under -plan, which changes nothing")
		}
		return nil
	}
	if *force {
		moved, err := q.Retry(queue.StageTranslate, queue.Done, queue.Failed, queue.Dead)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "asking again for %d pieces that were already finished\n", moved)
	}

	// The pool is built after the plan, because planning is free and finding out
	// there is no live route is not, and somebody running -plan should not be
	// made to wait on four probes to be told what would be asked.
	registry, source, err := flags.registry()
	if err != nil {
		return err
	}
	pool := route.NewPool(registry)
	pool.Prober = route.Prober{Timeout: *flags.limit}
	pool.Timeout = *flags.limit
	pool.Logf = func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
	engine.Pool = pool
	fmt.Fprintf(os.Stderr, "%d routes from %s, %d lanes\n\n", len(registry.Enabled()), source, pool.Lanes())

	started := time.Now()
	result, runErr := engine.Run(ctx, *group, *workers)
	fmt.Fprintf(os.Stderr, "\n%d pieces done, %d repaired, %d attempts failed, %d kept in English, %s\n",
		result.Done, result.Repaired, result.Failed, result.English,
		time.Since(started).Round(time.Second))
	usage := result.Usage.Normalized()
	if usage.TotalTokens > 0 {
		fmt.Fprintf(os.Stderr, "%d input tokens (%d cached), %d output\n",
			usage.InputTokens, usage.CachedInputTokens, usage.OutputTokens)
	}
	// Assemble even when the run stopped early, because the pages that did
	// finish are worth writing and the next run should not have to redo the
	// audit for them. A Ctrl-C halfway through a corpus run still ships whatever
	// came back whole.
	if err := report(engine.Assemble(pairs)); err != nil {
		return err
	}
	return runErr
}

// selectPairs works out which files this run is about.
func selectPairs(checkout string, paths []string, gap bool, group string) ([]content.Pair, error) {
	all, err := content.Root(checkout).Pairs()
	if err != nil {
		return nil, err
	}
	if gap {
		manifest, err := quality.LoadManifest(checkout)
		if err != nil {
			return nil, err
		}
		var out []content.Pair
		for _, pair := range all {
			stale, err := isStale(pair, manifest)
			if err != nil {
				return nil, err
			}
			if stale {
				out = append(out, pair)
			}
		}
		all = out
	}
	if group != "" {
		var out []content.Pair
		for _, pair := range all {
			if queue.GroupOf(pair.Rel) == group {
				out = append(out, pair)
			}
		}
		all = out
	}
	if len(paths) == 0 {
		return all, nil
	}
	var out []content.Pair
	for _, pair := range all {
		for _, want := range paths {
			want = strings.TrimPrefix(filepath.ToSlash(want), content.EnglishDir+"/")
			if pair.Rel == want || strings.HasPrefix(pair.Rel, strings.TrimSuffix(want, "/")+"/") {
				out = append(out, pair)
				break
			}
		}
	}
	return out, nil
}

// isStale is the -gap filter: no translation, no record of what it was made
// from, or a record that names an English file that has since moved.
//
// It is the same question L01 and L13 ask, put to one file rather than to a
// report, and it is deliberately generous. A file with no record is stale even
// if the translation looks fine, because there is no way to tell whether it
// does, and the corpus predates the manifest so today that is 557 of them. A run
// with -gap on a corpus with no manifest is a run over everything, which is the
// honest answer to the question it asked.
func isStale(pair content.Pair, manifest *quality.Manifest) (bool, error) {
	if !pair.Exists {
		return true, nil
	}
	record, ok := manifest.Get(pair.Rel)
	if !ok {
		return true, nil
	}
	english, err := pair.English()
	if err != nil {
		return false, err
	}
	return record.EnglishSHA256 != content.SHA256(english), nil
}

// report prints what assembly did.
func report(assembly translate.Assembly, err error) error {
	if err != nil {
		return err
	}
	notices, english := 0, 0
	for _, w := range assembly.Written {
		notices += w.Notices
		english += w.English
	}
	fmt.Fprintf(os.Stderr, "\n%d files written, %d notices to read", len(assembly.Written), notices)
	if english > 0 {
		fmt.Fprintf(os.Stderr, ", %d pieces left in English", english)
	}
	fmt.Fprintln(os.Stderr)

	if len(assembly.Waiting) > 0 {
		waiting := 0
		for _, n := range assembly.Waiting {
			waiting += n
		}
		fmt.Fprintf(os.Stderr, "%d files are waiting on %d pieces\n", len(assembly.Waiting), waiting)
	}
	if len(assembly.Refused) == 0 {
		return nil
	}

	fmt.Fprintf(os.Stderr, "\n%d files refused on the whole file:\n", len(assembly.Refused))
	refused := assembly.Refused
	sort.Slice(refused, func(i, j int) bool { return refused[i].Rel < refused[j].Rel })
	for _, r := range refused {
		fmt.Fprintf(os.Stderr, "  %s: %d findings, %d pieces sent back\n",
			r.Rel, len(r.Findings), len(r.Requeued))
		for _, f := range r.Findings {
			fmt.Fprintf(os.Stderr, "    %s\n", f)
		}
		// The unplaceable ones are the ones nobody will fix by running this
		// again, so they are called out rather than buried in the list above.
		for _, f := range r.Unplaced {
			fmt.Fprintf(os.Stderr, "    not traced to any piece: %s\n", f)
		}
	}
	return nil
}
