package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tamnd/godev-vn-translator/route"
)

// routeFlags are the three flags every command that talks to a model takes.
//
// They are here rather than repeated because a run that audits with one set of
// routes and translates with another is a run whose report is about something
// else.
type routeFlags struct {
	file  *string
	only  *string
	key   *string
	deep  *bool
	limit *time.Duration
}

func addRouteFlags(fs *flag.FlagSet) routeFlags {
	return routeFlags{
		file:  fs.String("routes", "", "route file, defaulting to $GODEV_ROUTES then ~/.config/godev/routes.json"),
		only:  fs.String("route", "", "use only these routes, comma separated, in this order"),
		key:   fs.String("key", "", "the proxy key, overriding the environment"),
		deep:  fs.Bool("deep", false, "ask each route a real question rather than only checking it is up"),
		limit: fs.Duration("timeout", 0, "per call timeout, overriding what each route declares"),
	}
}

// registry loads the routes and applies the flags, in that order, so that
// -route names something the file defines.
func (f routeFlags) registry() (route.Registry, string, error) {
	registry, source, err := route.LoadOrDefault(*f.file)
	if err != nil {
		return route.Registry{}, source, err
	}
	if names := strings.TrimSpace(*f.only); names != "" {
		registry, err = registry.Select(strings.Split(names, ","))
		if err != nil {
			return route.Registry{}, source, err
		}
		source += " (restricted)"
	}
	if key := strings.TrimSpace(*f.key); key != "" {
		for i := range registry.Routes {
			registry.Routes[i].APIKey = key
		}
	}
	return registry, source, nil
}

// runRoutes prints the table without asking anything, which is the command to
// run when the question is what would be tried and in what order.
func runRoutes(args []string) error {
	fs := flag.NewFlagSet("routes", flag.ExitOnError)
	flags := addRouteFlags(fs)
	write := fs.String("write", "", "write the routes to this file, for editing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	registry, source, err := flags.registry()
	if err != nil {
		return err
	}
	if path := strings.TrimSpace(*write); path != "" {
		if err := registry.Write(path); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", path)
		return nil
	}

	fmt.Printf("%d routes from %s\n\n", len(registry.Routes), source)
	fmt.Printf("%-4s  %-16s  %-8s  %-14s  %-5s  %s\n",
		"rank", "route", "kind", "model", "lanes", "note")
	for _, r := range registry.Routes {
		name := r.Name
		if r.Disabled {
			name += " (off)"
		}
		fmt.Printf("%-4d  %-16s  %-8s  %-14s  %-5d  %s\n",
			r.Rank, name, kindOf(r), r.Model, r.Lanes(), r.Note)
	}
	fmt.Printf("\ntotal lanes: %d\n", route.NewPool(registry).Lanes())
	return nil
}

// kindOf names the three kinds, because the difference is the first thing
// somebody reading the table wants and it is not otherwise visible.
func kindOf(r route.Route) string {
	switch {
	case r.IsCommand():
		return "command"
	case r.Gateway:
		return "gateway"
	default:
		return "box"
	}
}

// runDoctor probes every route and prints what it found.
//
// The shallow probe is the default because it is the one that can be run often.
// A box answers GET /v1/health in milliseconds with the size of its session
// pool, which is the thing that actually goes wrong: the sessions log
// themselves out, the host stays up, and every call it takes comes back with a
// refusal that reads like a model failure. Asking a real question costs
// two and a half minutes per box, which is fine once and useless as a guard.
func runDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	flags := addRouteFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	registry, source, err := flags.registry()
	if err != nil {
		return err
	}

	pool := route.NewPool(registry)
	pool.Prober = route.Prober{Deep: *flags.deep, Timeout: *flags.limit}
	fmt.Fprintf(os.Stderr, "probing %d routes from %s\n\n", len(registry.Enabled()), source)
	results := pool.ProbeAll(ctx)
	fmt.Print(route.Table(results))

	live := 0
	for _, r := range results {
		if r.State == route.StateLive {
			live++
		}
	}
	fmt.Printf("\n%d of %d routes are live, %d lanes\n", live, len(results), pool.Lanes())
	if live == 0 {
		return fmt.Errorf("no route is answering")
	}
	return nil
}
