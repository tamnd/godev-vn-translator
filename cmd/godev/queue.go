package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tamnd/godev-vn-translator/queue"
)

const queueUsage = `usage: godev queue <command> [flags]

commands:
  stats    count the jobs in every stage, and say how many leases have expired
  reap     return jobs whose worker did not come back
  retry    move failed and dead jobs back to pending with their attempts cleared
  drain    remove pending jobs, leaving the record of what happened
  list     print the jobs in one state, with the reason each one failed

flags:
  -root PATH    queue directory, default $GODEV_WORK/queue else work/<checkout>/queue
  -stage NAME   translate or repair, default every stage
  -state NAME   for list and retry, default dead
  -json         print jobs as JSON, for list

The queue is on disk because at two to ten minutes a call nothing finishes in
one process lifetime. It lives under work/, which is not committed: job state is
local and disposable, and the translated files are the durable thing.
`

// queueRoot is where the work list for one checkout lives.
//
// It is keyed on the checkout and not on the working directory. A job id is the
// content address of the work, so a chunk translated into one checkout is done
// and is skipped, and a second checkout sharing the queue would be skipped for
// work that was never written into it. That is not hypothetical: the same code
// in tamnd/bourbaki-solver used a plain relative path, ended up with one queue
// per directory anybody happened to run from, and enqueued ten pages twice.
//
// The path is inside this repo rather than inside the checkout because the
// checkout is a fork of golang/website and nothing here should be adding
// entries to its .gitignore. work/ is already ignored here.
func queueRoot(checkout string) string {
	if value := strings.TrimSpace(os.Getenv("GODEV_WORK")); value != "" {
		return filepath.Join(value, "queue")
	}
	abs, err := filepath.Abs(checkout)
	if err != nil {
		abs = checkout
	}
	sum := sha256.Sum256([]byte(abs))
	name := filepath.Base(abs) + "-" + hex.EncodeToString(sum[:4])
	return filepath.Join("work", name, "queue")
}

func runQueue(checkout string, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, queueUsage)
		os.Exit(2)
	}
	command := args[0]
	if command == "help" || command == "-h" || command == "--help" {
		fmt.Fprint(os.Stderr, queueUsage)
		return nil
	}
	fs := flag.NewFlagSet("queue "+command, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, queueUsage) }
	root := fs.String("root", queueRoot(checkout), "queue directory")
	stageName := fs.String("stage", "", "one stage, default every stage")
	stateName := fs.String("state", string(queue.Dead), "state, for list and retry")
	asJSON := fs.Bool("json", false, "print jobs as JSON, for list")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	q, err := queue.Open(*root)
	if err != nil {
		return err
	}
	stages := queue.Stages
	if strings.TrimSpace(*stageName) != "" {
		stage, err := queue.ParseStage(*stageName)
		if err != nil {
			return err
		}
		stages = []queue.Stage{stage}
	}

	switch command {
	case "stats":
		rows := make([]queue.Stats, 0, len(stages))
		for _, stage := range stages {
			stats, err := q.Stats(stage)
			if err != nil {
				return err
			}
			rows = append(rows, stats)
		}
		fmt.Print(queue.Table(rows))
		// An expired lease is not work in flight, it is a worker that died.
		// Saying so here saves somebody watching a stuck number for an hour.
		for _, row := range rows {
			if row.Expired > 0 {
				fmt.Fprintf(os.Stderr, "%s: %d leases have expired, run godev queue reap\n",
					row.Stage, row.Expired)
			}
		}
		return nil

	case "reap":
		for _, stage := range stages {
			reaped, err := q.Reap(stage)
			if err != nil {
				return err
			}
			if len(reaped) > 0 {
				fmt.Printf("%s: returned %d jobs to pending\n", stage, len(reaped))
			}
		}
		return nil

	case "retry":
		state, err := queue.ParseState(*stateName)
		if err != nil {
			return err
		}
		for _, stage := range stages {
			moved, err := q.Retry(stage, state)
			if err != nil {
				return err
			}
			if moved > 0 {
				fmt.Printf("%s: moved %d jobs from %s to pending\n", stage, moved, state)
			}
		}
		return nil

	case "drain":
		for _, stage := range stages {
			removed, err := q.Drain(stage)
			if err != nil {
				return err
			}
			if removed > 0 {
				fmt.Printf("%s: removed %d pending jobs\n", stage, removed)
			}
		}
		return nil

	case "list":
		state, err := queue.ParseState(*stateName)
		if err != nil {
			return err
		}
		for _, stage := range stages {
			jobs, err := q.List(stage, state)
			if err != nil {
				return err
			}
			for _, job := range jobs {
				if *asJSON {
					raw, err := json.Marshal(job)
					if err != nil {
						return err
					}
					fmt.Println(string(raw))
					continue
				}
				fmt.Printf("%s  %-9s  %s  attempts %d\n", job.ID, job.Stage, job.Target, job.Attempts)
				// Every attempt, not just the last. A chunk that failed on three
				// routes for three different reasons is a different problem from
				// one that failed the same way three times.
				for _, event := range job.History {
					fmt.Printf("    %s  %-10s  ok=%t  %s\n",
						event.TS.Format("2006-01-02 15:04:05"), event.Host, event.OK, event.Reason)
				}
			}
		}
		return nil

	default:
		return fmt.Errorf("unknown queue command %q", command)
	}
}
