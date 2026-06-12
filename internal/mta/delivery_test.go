package mta

import (
	"bufio"
	"fmt"
	"net"
	"net/textproto"
	"path/filepath"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/smtp"
	"hermex/internal/store"
)

// End-to-end: a message accepted over SMTP must land in the recipient's INBOX
// byte-faithfully, and an unknown recipient must be refused.
func TestSMTPToStoreDelivery(t *testing.T) {
	mboxPath := filepath.Join(t.TempDir(), "alice.sqlite3")
	accounts := directory.StaticAccounts{"alice@test": mboxPath}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &smtp.Server{Backend: &Backend{Accounts: accounts}, Hostname: "mail.test"}
	go srv.Serve(ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := textproto.NewReader(bufio.NewReader(conn))
	expect := func(code int) {
		t.Helper()
		if _, _, err := r.ReadResponse(code); err != nil {
			t.Fatalf("want %d: %v", code, err)
		}
	}

	expect(220)
	fmt.Fprint(conn, "EHLO client\r\n")
	expect(250)
	fmt.Fprint(conn, "MAIL FROM:<bob@external>\r\n")
	expect(250)
	fmt.Fprint(conn, "RCPT TO:<alice@test>\r\n")
	expect(250)
	// Unknown recipient is refused, not relayed.
	fmt.Fprint(conn, "RCPT TO:<nobody@test>\r\n")
	expect(550)
	fmt.Fprint(conn, "DATA\r\n")
	expect(354)
	msg := "Subject: hello\r\n\r\nhi alice\r\n"
	fmt.Fprintf(conn, "%s.\r\n", msg)
	expect(250)
	fmt.Fprint(conn, "QUIT\r\n")
	expect(221)

	st, err := store.Open(mboxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	inbox, ok, err := st.FolderByName(nil, "INBOX")
	if err != nil || !ok {
		t.Fatalf("INBOX lookup: ok=%v err=%v", ok, err)
	}
	msgs, err := st.ListMessages(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("delivered messages = %d, want 1", len(msgs))
	}
	raw, err := st.GetMessageRaw(inbox, msgs[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != msg {
		t.Errorf("stored message = %q, want %q", raw, msg)
	}
}
