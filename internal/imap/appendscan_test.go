package imap

import (
	"fmt"
	"strings"
	"testing"

	"hermex/internal/antivirus"
	"hermex/internal/avtest"
	"hermex/internal/directory"
	"hermex/internal/mta"
)

// scanDir is the directory capability the scanner gates on: one domain with
// inbound scanning enabled.
type scanDir struct{}

func (scanDir) GetDomainAVScan(string) (bool, bool, error)   { return true, false, nil }
func (scanDir) DomainID(string) (int64, bool, error)         { return 7, true, nil }
func (scanDir) DomainOrgAdminEmails(int64) ([]string, error) { return nil, nil }
func (scanDir) QuarantineMessage(directory.QuarantineEntry) (int64, error) {
	return 1, nil
}

// withScanner points the package-level scanner at a stub clamd for one test.
func withScanner(t *testing.T, verdict string) {
	t.Helper()
	sc, err := antivirus.New(avtest.Clamd(t, verdict))
	if err != nil {
		t.Fatal(err)
	}
	quar := t.TempDir()
	mta.SetScanner(sc, scanDir{}, func(int64) string { return quar + "/q.eml" }, "mail.test", nil)
	t.Cleanup(func() { mta.SetScanner(nil, nil, nil, "", nil) })
}

// appendMessage runs one APPEND and returns the tagged reply.
func appendMessage(t *testing.T, c *testClient, tag, msg string) string {
	t.Helper()
	_, _ = fmt.Fprintf(c.conn, "%s APPEND INBOX {%d}\r\n", tag, len(msg))
	if cont := c.line(); !strings.HasPrefix(cont, "+") {
		t.Fatalf("APPEND continuation = %q, want +", cont)
	}
	_, _ = fmt.Fprintf(c.conn, "%s\r\n", msg)
	return c.collectTagged(tag)
}

// TestAppendRefusesInfectedContent proves an APPEND literal is scanned. It is
// client-supplied content that never passes through delivery, so without the scan
// a mail client is a way to place malware in a mailbox unchecked.
func TestAppendRefusesInfectedContent(t *testing.T) {
	withScanner(t, avtest.Infected)
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX")

	reply := appendMessage(t, c, "a3", "Subject: infected\r\n\r\nMZ malware bytes")
	if !strings.HasPrefix(reply, "a3 NO") {
		t.Errorf("APPEND = %q, want NO", reply)
	}
	// Nothing was stored: the mailbox still holds the two seeded messages.
	c.mustOK("a4", "SELECT INBOX")
	status := c.taggedLine("a5", "STATUS INBOX (MESSAGES)")
	if !strings.Contains(status, "OK") {
		t.Fatalf("STATUS failed: %q", status)
	}
	if strings.Contains(status, "MESSAGES 3") {
		t.Error("the infected message was stored anyway")
	}
}

// TestAppendAcceptsCleanContent keeps ordinary APPEND working with the scanner on.
func TestAppendAcceptsCleanContent(t *testing.T) {
	withScanner(t, avtest.Clean)
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX")

	if reply := appendMessage(t, c, "a3", "Subject: fine\r\n\r\nbody"); !strings.HasPrefix(reply, "a3 OK") {
		t.Errorf("clean APPEND = %q, want OK", reply)
	}
}
