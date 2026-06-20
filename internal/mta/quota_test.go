package mta

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestRcptReceiveQuota proves an over-quota mailbox is refused at RCPT — a
// permanent rejection before the message is accepted, so there is no bounce
// backscatter — while an under-quota mailbox, and an unlimited (0) one, are
// accepted.
func TestRcptReceiveQuota(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1700000000, 0)
	raw := append([]byte("From: a@x\r\nTo: bob@local\r\nSubject: s\r\n\r\n"), bytes.Repeat([]byte("x"), 4096)...)
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, when, 0); err != nil {
		t.Fatal(err)
	}
	size, _ := st.MailboxSize()
	st.Close()

	accounts := directory.StaticAccounts{"bob@local": {MailboxPath: dir}}
	setQuota := func(kb uint32) {
		t.Helper()
		st, err := objectstore.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetQuota(objectstore.QuotaLimits{ReceiveKB: kb}); err != nil {
			t.Fatal(err)
		}
		st.Close()
	}

	// A receive quota above the current usage: accepted.
	setQuota(uint32(size/1024) + 100)
	if err := (&session{accounts: accounts}).Rcpt("bob@local"); err != nil {
		t.Errorf("under-quota Rcpt refused: %v", err)
	}

	// A receive quota below the current usage: refused permanently.
	setQuota(1)
	if err := (&session{accounts: accounts}).Rcpt("bob@local"); err == nil {
		t.Error("over-quota Rcpt accepted, want a permanent refusal")
	}

	// No quota (0 = unlimited): accepted regardless of usage.
	setQuota(0)
	if err := (&session{accounts: accounts}).Rcpt("bob@local"); err != nil {
		t.Errorf("unlimited Rcpt refused: %v", err)
	}
}
