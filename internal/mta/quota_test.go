package mta

import (
	"bytes"
	"errors"
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
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, when, 0)
	if err != nil || info.UID == 0 {
		t.Fatalf("AppendMessage: uid=%d err=%v", info.UID, err)
	}
	size, err := st.MailboxSize()
	mustNoErr(t, "mailbox size", err)
	mustClose(t, st)

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
		mustClose(t, st)
	}

	// A receive quota above the current usage: accepted.
	setQuota(uint32(size/1024) + 100)
	if err := (&session{accounts: accounts}).Rcpt("bob@local", smtp.RcptParams{}); err != nil {
		t.Errorf("under-quota Rcpt refused: %v", err)
	}

	// A receive quota below the current usage: refused permanently.
	setQuota(1)
	sOver := &session{accounts: accounts}
	err = sOver.Rcpt("bob@local", smtp.RcptParams{})
	if _, ok := errors.AsType[*smtp.PermError](err); !ok {
		t.Errorf("over-quota Rcpt = %v, want *smtp.PermError", err)
	}
	wantEq(t, "targets after receive-quota refusal", len(sOver.targets), 0)

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
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Unix(1700000000, 0), 0)
	if err != nil || info.UID == 0 {
		t.Fatalf("AppendMessage: uid=%d err=%v", info.UID, err)
	}
	mustClose(t, st)
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
	mustClose(t, st)
}

// TestMailSendQuota proves an authenticated submission is refused at MAIL FROM
// when the sender's own mailbox is over its send quota, while an under-quota
// sender and unauthenticated intake are not blocked.
func TestMailSendQuota(t *testing.T) {
	dir := fillMailbox(t)
	accounts := directory.StaticAccounts{"bob@local": {MailboxPath: dir}}

	setSendQuota(t, dir, 1) // 1 KiB, far below usage
	sOver := &session{accounts: accounts, authUser: "bob@local"}
	err := sOver.Mail("bob@local", smtp.MailParams{})
	if _, ok := errors.AsType[*smtp.PermError](err); !ok {
		t.Errorf("over-send-quota MAIL FROM = %v, want *smtp.PermError", err)
	}
	wantEq(t, "session from after send-quota refusal", sOver.from, "")

	setSendQuota(t, dir, 1<<20) // 1 GiB, above usage
	if err := (&session{accounts: accounts, authUser: "bob@local"}).Mail("bob@local", smtp.MailParams{}); err != nil {
		t.Errorf("under-send-quota MAIL FROM refused: %v", err)
	}

	// Unauthenticated intake is never blocked by send quota: an external sender
	// passes even while the local mailbox is over its quota.
	setSendQuota(t, dir, 1)
	if err := (&session{accounts: accounts}).Mail("someone@remote.example", smtp.MailParams{}); err != nil {
		t.Errorf("unauthenticated external MAIL FROM refused: %v", err)
	}
}

// TestDeliverAndRelaySendQuota proves an over-send-quota sender cannot submit
// through the shared user-send path, the chokepoint for EWS, MAPI, EAS, and
// webmail, while an automated Deliver (no relay) is not gated by send quota.
func TestDeliverAndRelaySendQuota(t *testing.T) {
	dir := fillMailbox(t)
	rcptDir := filepath.Join(t.TempDir(), "rcpt")
	accounts := directory.StaticAccounts{
		"bob@local":  {MailboxPath: dir},
		"rcpt@local": {MailboxPath: rcptDir},
	}
	when := time.Unix(1700000000, 0)

	setSendQuota(t, dir, 1)
	unresolved, err := DeliverAndRelay(accounts, nil, "bob@local", []string{"rcpt@local"}, []byte("hi"), when)
	if _, ok := errors.AsType[*smtp.PermError](err); !ok {
		t.Errorf("over-send-quota DeliverAndRelay = %v, want *smtp.PermError", err)
	}
	wantEq(t, "unresolved on send-quota refusal", len(unresolved), 1)
	wantEq(t, "recipient inbox after send-quota refusal", len(inboxMessagesIfAny(t, rcptDir)), 0)

	// Deliver (automated notifications) is never gated, so a bounce or auto-reply
	// from an over-quota mailbox still goes out.
	unresolved, err = Deliver(accounts, "bob@local", []string{"rcpt@local"}, []byte("hi"), when)
	if err != nil || len(unresolved) != 0 {
		t.Errorf("automated Deliver blocked by send quota: unresolved=%v err=%v", unresolved, err)
	}
}

// TestQuotaRecordsStoreCloseFailure proves a store Close failure during quota
// checks is recorded rather than dropped while keeping quota checks fail-open.
func TestQuotaRecordsStoreCloseFailure(t *testing.T) {
	dir := fillMailbox(t)
	accounts := directory.StaticAccounts{"bob@local": {MailboxPath: dir}}
	sink := withPassLogger(t)
	prevClose := closeMailbox
	closeMailbox = func(st *objectstore.Store) error {
		mustClose(t, st)
		return errors.New("quota close failed")
	}
	t.Cleanup(func() { closeMailbox = prevClose })

	if err := overReceiveQuota(dir); err != nil {
		t.Fatalf("overReceiveQuota under close fault = %v, want nil", err)
	}
	if err := overSendQuota(accounts, "bob@local"); err != nil {
		t.Fatalf("overSendQuota under close fault = %v, want nil", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	var ops []string
	for _, e := range sink.events {
		if e.Name == "store.close.fail" {
			op, ok := e.Fields["op"].(string)
			if !ok {
				t.Fatalf("op field type = %T, want string", e.Fields["op"])
			}
			ops = append(ops, op)
		}
	}
	if len(ops) != 2 || ops[0] != "over-receive-quota" || ops[1] != "over-send-quota" {
		t.Errorf("logged store.close.fail ops = %v, want [over-receive-quota over-send-quota]", ops)
	}
}
