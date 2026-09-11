package main

import (
	"slices"
	"testing"
)

// TestRecipientsOfNamesEveryAddressee proves the give-up report's fallback names
// who the message was for. The spooler hands the report an empty recipient list
// when it could not read the stored object, and the report loop then produces
// nothing at all, so the sender is never told their scheduled send was abandoned.
// The message's own headers still name its addressees, including the blind ones,
// which is what this recovers.
func TestRecipientsOfNamesEveryAddressee(t *testing.T) {
	raw := []byte("From: alice@hermex.test\r\n" +
		"To: to@example.com\r\n" +
		"Cc: cc@example.com\r\n" +
		"Bcc: bcc@example.com\r\n" +
		"Subject: scheduled\r\n" +
		"\r\n" +
		"body\r\n")

	got := recipientsOf(raw)
	for _, want := range []string{"to@example.com", "cc@example.com", "bcc@example.com"} {
		if !slices.Contains(got, want) {
			t.Errorf("the fallback recipient list %v is missing %s", got, want)
		}
	}
}

// TestAReportWithNoRecipientsFallsBackToTheMessage is the load-bearing case for
// the wiring. The spooler hands over no recipients when it could not read the
// stored object, and the report loop then runs zero times: the message is already
// in Drafts and the sender learns nothing. The report now names the addressees the
// message itself carries.
func TestAReportWithNoRecipientsFallsBackToTheMessage(t *testing.T) {
	raw := []byte("From: alice@hermex.test\r\nTo: to@example.com\r\nSubject: scheduled\r\n\r\nbody\r\n")

	got := reportRecipients(raw, nil)
	if !slices.Contains(got, "to@example.com") {
		t.Errorf("a report with no recipients named %v, want the message's own addressee", got)
	}
}

// TestTheStoredRecipientListWins keeps the fallback subordinate: the spooler's
// list comes from the stored object and carries the recipients delivery would
// have used, including any the headers do not show, so it is never second-guessed.
func TestTheStoredRecipientListWins(t *testing.T) {
	raw := []byte("From: alice@hermex.test\r\nTo: header@example.com\r\nSubject: scheduled\r\n\r\nbody\r\n")

	got := reportRecipients(raw, []string{"stored@example.com"})
	wantStored := len(got) == 1 && got[0] == "stored@example.com"
	if !wantStored {
		t.Errorf("reportRecipients = %v, want the stored list verbatim", got)
	}
}

// TestRecipientsOfInventsNoAddress keeps the fallback honest: a message it cannot
// parse yields no recipient rather than a made-up one, so a report is never sent
// to an address nobody addressed.
func TestRecipientsOfInventsNoAddress(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  []byte
	}{
		{"no headers at all", []byte("this is not a message")},
		{"no addressees", []byte("From: alice@hermex.test\r\nSubject: scheduled\r\n\r\nbody\r\n")},
		{"unparseable addressee", []byte("From: alice@hermex.test\r\nTo: @@@\r\n\r\nbody\r\n")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := recipientsOf(c.raw); len(got) != 0 {
				t.Errorf("recipientsOf = %v, want no recipient", got)
			}
		})
	}
}
