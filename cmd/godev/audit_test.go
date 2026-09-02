package main

import "testing"

// The pin is the number the checkout is allowed to have, so equal passes and
// one more fails. Both edges are asserted because that is the bug this would
// have.
func TestAuditStatus(t *testing.T) {
	cases := []struct {
		name     string
		refusals int
		max      int
		fails    bool
		says     bool
	}{
		{"no pin and nothing refused", 0, -1, false, false},
		{"no pin and one refused", 1, -1, true, false},
		{"exactly the pin", 227, 227, false, false},
		{"one over the pin", 228, 227, true, false},
		{"one under the pin", 226, 227, false, true},
		{"a pin of zero is the plain gate", 1, 0, true, false},
		{"a pin of zero on a clean corpus", 0, 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			slack, err := auditStatus(c.refusals, c.max)
			if fails := err != nil; fails != c.fails {
				t.Errorf("failed = %v, want %v (err %v)", fails, c.fails, err)
			}
			if says := slack != ""; says != c.says {
				t.Errorf("said %q, want a line: %v", slack, c.says)
			}
		})
	}
}
