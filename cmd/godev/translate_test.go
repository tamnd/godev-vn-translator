package main

import (
	"strings"
	"testing"
)

// The whole corpus is days of fleet time, so it has to be asked for. Everything
// that says what it is about, and everything that asks no route anything, goes
// through untouched.
func TestCheckScope(t *testing.T) {
	cases := []struct {
		name    string
		paths   []string
		gap     bool
		all     bool
		group   string
		free    bool
		refuses bool
	}{
		{name: "nothing at all", refuses: true},
		{name: "-all", all: true},
		{name: "-gap", gap: true},
		{name: "-group", group: "blog"},
		{name: "one path", paths: []string{"ref/mod.md"}},
		{name: "-plan or -assemble", free: true},
		// -plan over the whole corpus is how somebody finds out what a full run
		// would cost, and making them pass -all to be told that is a gate in
		// front of the thing that stops the mistake.
		{name: "-plan with no scope", free: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkScope(c.paths, c.gap, c.all, c.group, c.free)
			if refuses := err != nil; refuses != c.refuses {
				t.Fatalf("refused = %v, want %v (err %v)", refuses, c.refuses, err)
			}
			if err == nil {
				return
			}
			// The message is the whole value of the check. A refusal that does
			// not say what to type instead is a refusal somebody works around
			// by passing the first flag they can find.
			for _, want := range []string{"-gap", "-group", "-all", "-plan"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not mention %s:\n%v", want, err)
				}
			}
		})
	}
}
