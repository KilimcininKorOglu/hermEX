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

// TestArityBoundsAreCoherent covers the check that stands between a short
// command line and an index out of range in the run function.
func TestArityBoundsAreCoherent(t *testing.T) {
	for _, c := range commands {
		if c.maxArgs > 0 && c.minArgs > c.maxArgs {
			t.Errorf("%q accepts nothing: min %d > max %d", c.name, c.minArgs, c.maxArgs)
		}
		if c.minArgs == 0 && c.maxArgs == 0 {
			continue // deliberately unchecked
		}
		// A command whose help names an argument has to require it, or the run
		// function reads past the end of the slice.
		if strings.Contains(c.args, "<") && c.minArgs < 2 {
			t.Errorf("%q names a required argument but accepts %d", c.name, c.minArgs)
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
