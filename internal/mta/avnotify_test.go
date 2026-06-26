package mta

import (
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
