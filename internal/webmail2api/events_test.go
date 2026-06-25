package webmail2api

import "testing"

// TestInboxDelta proves the SSE stream fires the right event for each kind of inbox
// change and stays silent otherwise: a first observation only baselines, more mail
// is new_mail, a delete or a read/unread shift is a folder_update, and an unchanged
// poll fires nothing so the SPA is never told to refetch for no reason.
func TestInboxDelta(t *testing.T) {
	cases := []struct {
		name                                 string
		prevTotal, prevUnread, total, unread int
		want                                 string
	}{
		{"baseline first observation never fires", -1, -1, 5, 2, ""},
		{"new mail raises the total", 5, 2, 6, 3, "new_mail"},
		{"a delete lowers the total", 6, 3, 5, 3, "folder_update"},
		{"reading lowers unread only", 5, 3, 5, 1, "folder_update"},
		{"an unchanged poll is silent", 5, 1, 5, 1, ""},
	}
	for _, c := range cases {
		if got := inboxDelta(c.prevTotal, c.prevUnread, c.total, c.unread); got != c.want {
			t.Errorf("%s: inboxDelta(%d,%d,%d,%d) = %q, want %q",
				c.name, c.prevTotal, c.prevUnread, c.total, c.unread, got, c.want)
		}
	}
}
