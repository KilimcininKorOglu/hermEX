package directory

import "testing"

// TestMayReachFailsClosed proves the reach check before any folder grant: one's
// own mailbox is always reachable, another mailbox only within the scope, and a
// directory with no scope model reaches no one else.
func TestMayReachFailsClosed(t *testing.T) {
	accs := StaticAccounts{"a@one.test": {}, "b@one.test": {}, "c@two.test": {}}
	for _, c := range []struct {
		dir           any
		caller, other string
		want          bool
	}{
		{accs, "a@one.test", "A@One.test", true},
		{accs, "a@one.test", "b@one.test", true},
		{accs, "a@one.test", "c@two.test", false},
		{accs, "a@one.test", "ghost@one.test", false},
		{struct{}{}, "a@one.test", "b@one.test", false},
		{struct{}{}, "a@one.test", "a@one.test", true},
	} {
		got, err := MayReach(c.dir, c.caller, c.other)
		if err != nil || got != c.want {
			t.Errorf("MayReach(%T, %s, %s) = %v, %v; want %v", c.dir, c.caller, c.other, got, err, c.want)
		}
	}
}
