package dav

import (
	"strings"
	"testing"
)

// TestITIPMailNamesItsSender pins the identity every scheduling message carries.
// buildITIP stores only a sender, and the mail export writes From from the
// representing identity, so each iTIP message the implicit-scheduling broker and the
// scheduling outbox produced went out with no From header at all. A REPLY that names
// nobody cannot be matched to an attendee, and a REQUEST that names nobody is refused
// or filed as junk by the receiving server.
func TestITIPMailNamesItsSender(t *testing.T) {
	raw, err := buildITIP("alice@hermex.test", []string{"mailto:boss@external.test"},
		"Accepted: Budget", "BEGIN:VCALENDAR\r\nMETHOD:REPLY\r\nEND:VCALENDAR\r\n", "REPLY")
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	if !strings.Contains(head, "From: <alice@hermex.test>") {
		t.Errorf("the iTIP mail carries no From naming its sender:\n%s", head)
	}
}
