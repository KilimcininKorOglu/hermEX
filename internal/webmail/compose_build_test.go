package webmail

import (
	"strings"
	"testing"
)

// TestBuildMessageDeliveredFormHasIDAndDateNoBcc checks the delivered wire form
// (buildMessage output, before any Bcc splice): it carries a Message-ID anchored
// to the sending host and a Date, and never a Bcc header — Bcc is spliced onto
// the stored copy only via insertBcc. Message-ID and Date come from the oxcmail
// export path, which has no synthesis fallback, so composeToMessage must set them
// or the delivered copy (not re-imported before an external relay) would lose
// them.
func TestBuildMessageDeliveredFormHasIDAndDateNoBcc(t *testing.T) {
	raw, err := buildMessage(outgoing{
		From:     "alice@hermex.test",
		To:       "bob@example.com",
		Cc:       "carol@example.com",
		Subject:  "delivered form",
		Body:     "hello",
		Hostname: "mail.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if id := headerValue(s, "Message-ID"); !strings.Contains(id, "@mail.test") {
		t.Errorf("delivered message has no host-anchored Message-ID (got %q):\n%s", id, s)
	}
	if headerValue(s, "Date") == "" {
		t.Errorf("delivered message has no Date header:\n%s", s)
	}
	if bcc := headerValue(s, "Bcc"); bcc != "" {
		t.Errorf("delivered message must not carry a Bcc header (got %q):\n%s", bcc, s)
	}
	if !strings.Contains(s, "bob@example.com") || !strings.Contains(s, "carol@example.com") {
		t.Errorf("delivered message lost its To/Cc recipients:\n%s", s)
	}
}
