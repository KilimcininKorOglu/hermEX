package main

import (
	"testing"

	"hermex/internal/mapi"
)

// TestResolveFolderArgTakesANameOrAnID covers what an operator types. A name is
// what they reach for; a numeric id is the only handle a user-created folder
// has, because its name is whatever the user called it.
func TestResolveFolderArgTakesANameOrAnID(t *testing.T) {
	for _, c := range []struct {
		arg  string
		want int64
	}{
		{"inbox", int64(mapi.PrivateFIDInbox)},
		{"Inbox", int64(mapi.PrivateFIDInbox)},
		{" JUNK ", int64(mapi.PrivateFIDJunk)},
		{"spam", int64(mapi.PrivateFIDJunk)}, // the two names an operator uses for it
		{"trash", int64(mapi.PrivateFIDDeletedItems)},
		{"deleted", int64(mapi.PrivateFIDDeletedItems)},
		{"524288", 524288},
	} {
		got, err := resolveFolderArg(c.arg)
		if err != nil || got != c.want {
			t.Errorf("resolveFolderArg(%q) = %d, %v; want %d", c.arg, got, err, c.want)
		}
	}
}

// TestResolveFolderArgRefusesTheRest keeps a typo from being read as a folder.
// An unknown name silently resolving to something would move mail into a folder
// the operator did not name.
func TestResolveFolderArgRefusesTheRest(t *testing.T) {
	for _, arg := range []string{"", "  ", "inbx", "Projects", "-1", "0", "1.5"} {
		if fid, err := resolveFolderArg(arg); err == nil {
			t.Errorf("resolveFolderArg(%q) resolved to %d", arg, fid)
		}
	}
}

// TestParseUIDsRefusesAnythingButAPositiveNumber covers the message ids. A zero
// or a negative one names no message, and a silent conversion would report a
// move that never happened.
func TestParseUIDsRefusesAnythingButAPositiveNumber(t *testing.T) {
	uids, err := parseUIDs([]string{"1", " 42 ", "4294967295"})
	if err != nil {
		t.Fatalf("parseUIDs: %v", err)
	}
	if len(uids) != 3 || uids[0] != 1 || uids[1] != 42 || uids[2] != 4294967295 {
		t.Errorf("uids = %v", uids)
	}
	for _, bad := range [][]string{{"0"}, {"-1"}, {"abc"}, {""}, {"4294967296"}, {"1", "x"}} {
		if _, err := parseUIDs(bad); err == nil {
			t.Errorf("parseUIDs(%v) was accepted", bad)
		}
	}
}
