package mta

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
	"hermex/internal/smtp"
)

// End-to-end: a message accepted over SMTP must land in the recipient's inbox
// and be served back as a semantically faithful message (subject and body
// preserved through the store's parse/re-synthesize round trip), and an unknown
// recipient must be refused.
func TestSMTPToStoreDelivery(t *testing.T) {
	mboxPath := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mboxPath}}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, "listen", err)
	srv := &smtp.Server{Backend: &Backend{Accounts: accounts}, Hostname: "mail.test"}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	t.Cleanup(func() {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close listener: %v", err)
		}
		if err := <-serveErr; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("smtp serve: %v", err)
		}
	})

	conn, err := net.Dial("tcp", ln.Addr().String())
	mustNoErr(t, "dial", err)
	t.Cleanup(func() { mustClose(t, conn) })
	r := textproto.NewReader(bufio.NewReader(conn))
	expect := func(code int) {
		t.Helper()
		if _, _, err := r.ReadResponse(code); err != nil {
			t.Fatalf("want %d: %v", code, err)
		}
	}

	write := func(s string) {
		t.Helper()
		if _, err := fmt.Fprint(conn, s); err != nil {
			t.Fatalf("write %q: %v", s, err)
		}
	}

	expect(220)
	write("EHLO client\r\n")
	expect(250)
	write("MAIL FROM:<bob@external>\r\n")
	expect(250)
	write("RCPT TO:<alice@test>\r\n")
	expect(250)
	// Unknown recipient is refused, not relayed.
	write("RCPT TO:<nobody@test>\r\n")
	expect(550)
	write("DATA\r\n")
	expect(354)
	msg := "Subject: hello\r\n\r\nhi alice\r\n"
	write(msg + ".\r\n")
	expect(250)
	write("QUIT\r\n")
	expect(221)

	st, err := objectstore.Open(mboxPath)
	mustNoErr(t, "open the mailbox", err)
	defer mustClose(t, st)
	inbox := int64(mapi.PrivateFIDInbox)
	msgs, err := st.ListMessages(inbox)
	mustNoErr(t, "list the inbox", err)
	if len(msgs) != 1 {
		t.Fatalf("delivered messages = %d, want 1", len(msgs))
	}
	raw, err := st.GetMessageRaw(inbox, msgs[0].UID)
	mustNoErr(t, "read the delivered message", err)
	// The served message is re-synthesized from the stored MAPI object, so it
	// is not byte-identical to arrival; assert the subject and body survived.
	env, err := mime.ParseEnvelope(raw)
	mustNoErr(t, "parse the delivered message", err)
	wantEq(t, "the subject", env.Subject, "hello")
	if !bytes.Contains(raw, []byte("hi alice")) {
		t.Errorf("body text not preserved in %q", raw)
	}
}

// TestAStoreCloseFaultAfterDeliveryIsRecordedNotRetried closes the mailbox with a
// fault after the message was committed. The message is already filed, so a
// temporary failure would make the sender retry it into a second copy (RFC 5321
// 4.5.4.1 retries until a 250); delivery succeeds once and the fault reaches the
// log as store.close.fail.
func TestAStoreCloseFaultAfterDeliveryIsRecordedNotRetried(t *testing.T) {
	sink := withPassLogger(t)
	mbox := filepath.Join(t.TempDir(), "alice")
	accounts := directory.StaticAccounts{"alice@test": {MailboxPath: mbox}}
	prevClose := closeMailbox
	closeMailbox = func(st *objectstore.Store) error {
		mustClose(t, st)
		return errors.New("disk flush failed on close")
	}
	t.Cleanup(func() { closeMailbox = prevClose })

	unresolved, err := Deliver(accounts, "bob@external", []string{"alice@test"}, []byte("Subject: hi\r\n\r\nbody"), time.Now())
	mustNoErr(t, "deliver with a close fault", err)
	wantEq(t, "unresolved recipients", len(unresolved), 0)

	closeMailbox = prevClose
	st, err := objectstore.OpenExisting(mbox)
	mustNoErr(t, "open the mailbox", err)
	defer mustClose(t, st)
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list the inbox", err)
	wantEq(t, "messages filed", len(msgs), 1)

	ev, ok := sink.find("store.close.fail")
	if !ok {
		t.Fatal("the close fault emitted no store.close.fail event")
	}
	if !strings.Contains(ev.Err, "disk flush failed on close") {
		t.Errorf("store.close.fail Err = %q, want the close error", ev.Err)
	}
}
