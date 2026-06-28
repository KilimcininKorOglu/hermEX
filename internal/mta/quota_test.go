package mta

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/smtp"
)

// TestRcptReceiveQuota proves an over-quota mailbox is refused at RCPT, a
// permanent rejection before the message is accepted, so there is no bounce
// backscatter, while an under-quota mailbox, and an unlimited (0) one, are
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
	if err := (&session{accounts: accounts}).Rcpt("bob@local", smtp.RcptParams{}); err != nil {
		t.Errorf("under-quota Rcpt refused: %v", err)
	}

	// A receive quota below the current usage: refused permanently.
	setQuota(1)
	if err := (&session{accounts: accounts}).Rcpt("bob@local", smtp.RcptParams{}); err == nil {
		t.Error("over-quota Rcpt accepted, want a permanent refusal")
	}

	// No quota (0 = unlimited): accepted regardless of usage.
	setQuota(0)
	if err := (&session{accounts: accounts}).Rcpt("bob@local", smtp.RcptParams{}); err != nil {
		t.Errorf("unlimited Rcpt refused: %v", err)
	}
}

// fillMailbox creates a mailbox at a fresh dir, files one ~4 KiB message, and
// returns the dir so a send-quota test can set a limit below it.
func fillMailbox(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte("From: bob@local\r\nTo: x@y\r\nSubject: s\r\n\r\n"), bytes.Repeat([]byte("x"), 4096)...)
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Unix(1700000000, 0), 0); err != nil {
		t.Fatal(err)
	}
	st.Close()
	return dir
}

func setSendQuota(t *testing.T, dir string, kb uint32) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuota(objectstore.QuotaLimits{SendKB: kb}); err != nil {
		t.Fatal(err)
	}
	st.Close()
}

// TestMailSendQuota proves an authenticated submission is refused at MAIL FROM
// when the sender's own mailbox is over its send quota, while an under-quota
// sender and unauthenticated intake are not blocked.
func TestMailSendQuota(t *testing.T) {
	dir := fillMailbox(t)
	accounts := directory.StaticAccounts{"bob@local": {MailboxPath: dir}}

	setSendQuota(t, dir, 1) // 1 KiB, far below usage
	if err := (&session{accounts: accounts, authUser: "bob@local"}).Mail("bob@local", smtp.MailParams{}); err == nil {
		t.Error("over-send-quota MAIL FROM accepted, want a refusal")
	}

	setSendQuota(t, dir, 1<<20) // 1 GiB, above usage
	if err := (&session{accounts: accounts, authUser: "bob@local"}).Mail("bob@local", smtp.MailParams{}); err != nil {
		t.Errorf("under-send-quota MAIL FROM refused: %v", err)
	}

	// Unauthenticated intake is never blocked by send quota, the local user is
	// not the one sending.
	setSendQuota(t, dir, 1)
	if err := (&session{accounts: accounts}).Mail("bob@local", smtp.MailParams{}); err != nil {
		t.Errorf("unauthenticated MAIL FROM refused: %v", err)
	}
}

// TestDeliverAndRelaySendQuota proves an over-send-quota sender cannot submit
// through the shared user-send path, the chokepoint for EWS, MAPI, EAS, and
// webmail, while an automated Deliver (no relay) is not gated by send quota.
func TestDeliverAndRelaySendQuota(t *testing.T) {
	dir := fillMailbox(t)
	accounts := directory.StaticAccounts{"bob@local": {MailboxPath: dir}}
	when := time.Unix(1700000000, 0)

	setSendQuota(t, dir, 1)
	if _, err := DeliverAndRelay(accounts, nil, "bob@local", []string{"x@y"}, []byte("hi"), when); err == nil {
		t.Error("over-send-quota DeliverAndRelay accepted, want a refusal")
	}
	// Deliver (automated notifications) is never gated, so a bounce or auto-reply
	// from an over-quota mailbox still goes out.
	if _, err := Deliver(accounts, "bob@local", []string{"x@y"}, []byte("hi"), when); err != nil {
		t.Errorf("automated Deliver blocked by send quota: %v", err)
	}
}
