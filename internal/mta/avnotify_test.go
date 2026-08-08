package mta

import (
	"slices"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

func TestBuildQuarantineNoticeInbound(t *testing.T) {
	rec := directory.QuarantineRecord{
		QuarantineEntry: directory.QuarantineEntry{
			Direction:    "inbound",
			MailFrom:     "evil@spam.example",
			Subject:      "invoice",
			VirusName:    "Eicar-Test-Signature",
			InfectedFile: "invoice.exe",
		},
	}
	notice := string(buildQuarantineNotice(rec, "victim@acme.test", "mail.acme.test", time.Unix(1000, 0)))

	for _, want := range []string{
		"To: victim@acme.test",
		"Auto-Submitted: auto-generated",
		"Content-Type: text/plain; charset=utf-8",
		"Eicar-Test-Signature",
		"invoice.exe",
		"evil@spam.example",
		"karantinaya alındı",
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice missing %q\n%s", want, notice)
		}
	}
	// Text-only: no attachment carries the infected bytes.
	if strings.Contains(notice, "multipart") || strings.Contains(notice, "application/octet-stream") {
		t.Error("notice must be text-only, no attachment")
	}
}

func TestBuildQuarantineNoticeOutbound(t *testing.T) {
	rec := directory.QuarantineRecord{
		QuarantineEntry: directory.QuarantineEntry{
			Direction: "outbound",
			MailFrom:  "user@acme.test",
			Subject:   "report",
			VirusName: "Win.Test.EICAR",
		},
	}
	notice := string(buildQuarantineNotice(rec, "user@acme.test", "mail.acme.test", time.Unix(1000, 0)))
	if !strings.Contains(notice, "Gönderdiğiniz") {
		t.Errorf("outbound notice should use sender wording\n%s", notice)
	}
	// No infected file means no file clause.
	if strings.Contains(notice, "dosyasındaki") {
		t.Errorf("notice names a file when none was set\n%s", notice)
	}
}

// TestQuarantineNoticeFlattensUntrusted proves a CRLF-bearing subject is
// flattened so it cannot disrupt the notice body.
func TestQuarantineNoticeFlattensUntrusted(t *testing.T) {
	rec := directory.QuarantineRecord{
		QuarantineEntry: directory.QuarantineEntry{
			Direction: "inbound",
			MailFrom:  "a@b.test",
			Subject:   "line1\r\nInjected: header",
			VirusName: "X",
		},
	}
	body := quarantineNoticeText(rec)
	if !strings.Contains(body, "line1 Injected: header") {
		t.Errorf("subject CRLF not flattened: %q", body)
	}
}

// TestQuarantineNoticeFailureIsReported proves an undeliverable notice reaches
// the caller's report hook. The notice is how a user and the domain's admins
// learn a message was caught, so a run of failures that leaves the log showing
// nothing but clean quarantines is exactly the state this hook exists to prevent.
func TestQuarantineNoticeFailureIsReported(t *testing.T) {
	// No account resolves, so every notice fails to find a local mailbox.
	accounts := directory.StaticAccounts{}
	rec := directory.QuarantineRecord{
		ID: 42,
		QuarantineEntry: directory.QuarantineEntry{
			Direction: "inbound", MailFrom: "evil@spam.example", VirusName: "Eicar-Test-Signature",
		},
	}

	var reported []string
	notifyQuarantine(accounts, rec, []string{"victim@acme.test"}, []string{"admin@acme.test"},
		"mail.acme.test", time.Unix(1000, 0),
		func(rcpt string, unresolved []string, err error) {
			reported = append(reported, rcpt)
		})

	if len(reported) != 2 {
		t.Fatalf("reported %d failed notices, want 2 (the user and the admin)", len(reported))
	}
	for _, want := range []string{"victim@acme.test", "admin@acme.test"} {
		if !slices.Contains(reported, want) {
			t.Errorf("no failure reported for %s: %v", want, reported)
		}
	}
}

// TestQuarantineNoticeSuccessIsQuiet holds the other half: the hook fires on
// failure only, so a working deployment logs nothing per notice.
func TestQuarantineNoticeSuccessIsQuiet(t *testing.T) {
	mbox := t.TempDir()
	accounts := directory.StaticAccounts{"victim@acme.test": {Password: "pw", MailboxPath: mbox}}
	rec := directory.QuarantineRecord{ID: 7, QuarantineEntry: directory.QuarantineEntry{Direction: "inbound"}}

	called := 0
	notifyQuarantine(accounts, rec, []string{"victim@acme.test"}, nil, "mail.acme.test", time.Unix(1000, 0),
		func(string, []string, error) { called++ })

	if called != 0 {
		t.Errorf("a delivered notice reported %d failure(s)", called)
	}
}
