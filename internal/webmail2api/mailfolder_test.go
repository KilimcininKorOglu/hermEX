package webmail2api

import "testing"

// TestFilterMail proves the server-side folder filter keeps only unread or starred
// rows and leaves the set unchanged for "all"/unknown.
func TestFilterMail(t *testing.T) {
	rows := []mailJSON{
		{ID: "a", Read: true, Starred: false},
		{ID: "b", Read: false, Starred: true},
		{ID: "c", Read: false, Starred: false},
	}
	if got := filterMail(rows, "unread"); len(got) != 2 || got[0].ID != "b" || got[1].ID != "c" {
		t.Errorf("unread filter = %+v, want b,c", got)
	}
	if got := filterMail(rows, "starred"); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("starred filter = %+v, want b", got)
	}
	if got := filterMail(rows, "all"); len(got) != 3 {
		t.Errorf("all filter dropped rows: %+v", got)
	}
}

// TestSortMail proves the server-side ordering by date/from/subject/size and
// direction, with date-descending as the default (newest first).
func TestSortMail(t *testing.T) {
	rows := []mailJSON{
		{ID: "old", Date: "2020-01-01T00:00:00Z", From: "carol@x", Subject: "Beta", Size: 30},
		{ID: "new", Date: "2026-01-01T00:00:00Z", From: "alice@x", Subject: "Alpha", Size: 10},
		{ID: "mid", Date: "2023-01-01T00:00:00Z", From: "bob@x", Subject: "Gamma", Size: 20},
	}
	order := func(field, dir string) string {
		cp := append([]mailJSON(nil), rows...)
		sortMail(cp, field, dir)
		return cp[0].ID + "," + cp[1].ID + "," + cp[2].ID
	}
	cases := []struct {
		field, dir, want string
	}{
		{"date", "", "new,mid,old"},       // default desc = newest first
		{"date", "asc", "old,mid,new"},    // oldest first
		{"from", "asc", "new,mid,old"},    // alice, bob, carol
		{"subject", "asc", "new,old,mid"}, // Alpha, Beta, Gamma
		{"size", "desc", "old,mid,new"},   // 30, 20, 10
	}
	for _, c := range cases {
		if got := order(c.field, c.dir); got != c.want {
			t.Errorf("sort %s/%s = %s, want %s", c.field, c.dir, got, c.want)
		}
	}
}
