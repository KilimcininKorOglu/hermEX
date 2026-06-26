package imap

import (
	"strings"
	"testing"
)

// TestIMAPIDUnselectCapability covers the cheap RFC additions: the CHILDREN/ID/
// UNSELECT capabilities, ID returning the server name in any state, and UNSELECT
// returning to the authenticated state WITHOUT expunging \Deleted messages.
func TestIMAPIDUnselectCapability(t *testing.T) {
	c, _ := startServer(t)

	caps := strings.Join(c.mustOK("a1", "CAPABILITY"), " ")
	for _, want := range []string{"CHILDREN", "ID", "UNSELECT"} {
		if !strings.Contains(caps, want) {
			t.Errorf("CAPABILITY missing %q: %s", want, caps)
		}
	}

	// ID is valid in any state (here before login) and returns the server name.
	un, status := c.do("a2", `ID ("name" "TestClient")`)
	if status != "OK" {
		t.Fatalf("ID status = %s, want OK", status)
	}
	if !strings.Contains(strings.Join(un, " "), `"name" "hermEX"`) {
		t.Errorf("ID response = %v, want the server name", un)
	}

	// UNSELECT needs a selected mailbox.
	if _, status := c.do("a3", "UNSELECT"); status != "NO" {
		t.Errorf("UNSELECT with no mailbox = %s, want NO", status)
	}

	// Mark a message \Deleted, then UNSELECT: it must NOT be expunged, so a
	// re-SELECT still reports both messages (CLOSE would have expunged it).
	c.mustOK("a4", "LOGIN alice secret")
	c.mustOK("a5", "SELECT INBOX")
	c.mustOK("a6", `STORE 1 +FLAGS (\Deleted)`)
	c.mustOK("a7", "UNSELECT")
	reselect := strings.Join(c.mustOK("a8", "SELECT INBOX"), " ")
	if !strings.Contains(reselect, "2 EXISTS") {
		t.Errorf("after UNSELECT, SELECT = %q, want 2 EXISTS (no expunge)", reselect)
	}
}
