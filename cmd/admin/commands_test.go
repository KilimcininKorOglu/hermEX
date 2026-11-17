package main

import (
	"strings"
	"testing"
)

// TestEveryCommandIsDescribed keeps the help honest. The table is the single
// source for both the dispatch and the help text, and this is what says so: a
// command with no run function dispatches to nothing, and one with no name
// cannot be typed.
func TestEveryCommandIsDescribed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range commands {
		if c.name == "" {
			t.Error("a command carries no name")
			continue
		}
		if seen[c.name] {
			t.Errorf("%q appears twice; the second one is unreachable", c.name)
		}
		seen[c.name] = true
		if c.run == nil {
			t.Errorf("%q has no run function, so typing it does nothing", c.name)
		}
		if strings.TrimSpace(c.args) != c.args || strings.TrimSpace(c.note) != c.note {
			t.Errorf("%q has padded help text, which the usage line adds itself", c.name)
		}
	}
}

// TestLookupFindsExactlyTheTable is the dispatch itself: a name in the table
// resolves, and one outside it does not, which is what sends the caller to the
// help.
func TestLookupFindsExactlyTheTable(t *testing.T) {
	for _, c := range commands {
		got, ok := lookup(c.name)
		if !ok || got.name != c.name {
			t.Errorf("lookup(%q) = %q, %v", c.name, got.name, ok)
		}
	}
	for _, name := range []string{"", "serv", "create-users", "--help"} {
		if _, ok := lookup(name); ok {
			t.Errorf("lookup(%q) resolved", name)
		}
	}
}

// helpArity reads the bounds a command's help text promises, counting the
// command name: each <arg> is required, each [arg] optional, and a trailing
// "..." on an argument lifts the upper bound.
func helpArity(args string) (lo, hi int) {
	lo, hi = 1, 1
	variadic := false
	for tok := range strings.FieldsSeq(args) {
		switch {
		case strings.HasPrefix(tok, "<"):
			lo++
			hi++
		case strings.HasPrefix(tok, "["):
			hi++
		}
		variadic = variadic || strings.HasSuffix(tok, "...")
	}
	if args == "" {
		return 0, 0 // a command with no arguments is left unchecked
	}
	if variadic {
		hi = 0
	}
	return lo, hi
}

// TestArityBoundsAreCoherent holds each command's argument bounds to what its
// help text tells the operator to type. The bounds are the check that stands
// between a short command line and an index out of range in the run function:
// a command whose help names two required arguments but accepts one reads past
// the end of its slice.
func TestArityBoundsAreCoherent(t *testing.T) {
	for _, c := range commands {
		lo, hi := helpArity(c.args)
		if c.minArgs != lo || c.maxArgs != hi {
			t.Errorf("%q (%q) takes %d..%d arguments, its help promises %d..%d", c.name, c.args, c.minArgs, c.maxArgs, lo, hi)
		}
	}
}

// TestHelpArityReadsTheNotation pins the reading TestArityBoundsAreCoherent
// relies on, against help strings written for this test.
func TestHelpArityReadsTheNotation(t *testing.T) {
	for args, want := range map[string][2]int{
		"":                              {0, 0},
		"<email>":                       {2, 2},
		"<email> <password>":            {3, 3},
		"<email> [days]":                {2, 3},
		"<email> <a> <b> <uid>... [-c]": {5, 0},
	} {
		if lo, hi := helpArity(args); lo != want[0] || hi != want[1] {
			t.Errorf("helpArity(%q) = %d..%d, want %d..%d", args, lo, hi, want[0], want[1])
		}
	}
}

// TestAcceptsHonoursTheBounds checks the predicate the dispatch gates on.
func TestAcceptsHonoursTheBounds(t *testing.T) {
	c := command{minArgs: 2, maxArgs: 3}
	for n, want := range map[int]bool{1: false, 2: true, 3: true, 4: false} {
		if got := c.accepts(n); got != want {
			t.Errorf("accepts(%d) = %v, want %v", n, got, want)
		}
	}
	// An unchecked command takes whatever it is given.
	open := command{}
	for _, n := range []int{1, 5, 50} {
		if !open.accepts(n) {
			t.Errorf("an unchecked command refused %d arguments", n)
		}
	}
	// A minimum with no maximum has no upper bound.
	atLeast := command{minArgs: 2}
	if atLeast.accepts(1) || !atLeast.accepts(9) {
		t.Error("a minimum-only command did not bound only below")
	}
}
