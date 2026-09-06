package smtp

import (
	"net"
	"net/textproto"
	"testing"
)

// TestServerRefusesANonASCIIAddressWithoutSMTPUTF8 pins RFC 6531 §3.5: the
// extension is negotiated on MAIL, so an address carrying non-ASCII bytes is
// only permitted once the client asked for it. Accepting one without the keyword
// lets an address into the queue that this server never promised a next hop
// could receive, and the requirement is not recoverable downstream because
// nothing records that the transaction needed it.
//
// The reply codes are the ones RFC 6531 §3.5 assigns: 550 on MAIL (the sender's
// mailbox is unavailable), 553 on RCPT (the recipient's name is not allowed),
// both with the enhanced code 5.6.7.
func TestServerRefusesANonASCIIAddressWithoutSMTPUTF8(t *testing.T) {
	const utf8From = "posıta@örnek.test"
	const utf8Rcpt = "alıcı@örnek.test"

	t.Run("MAIL without the keyword", func(t *testing.T) {
		sess := &fakeSession{}
		r, conn := dialServer(t, sess)
		expect(t, r, 220)
		ehlo(t, r, conn)

		send(t, conn, "MAIL FROM:<"+utf8From+">\r\n")
		expect(t, r, 550)
		if sess.from != "" {
			t.Errorf("the refused sender still reached the backend: %q", sess.from)
		}
	})

	t.Run("RCPT without the keyword", func(t *testing.T) {
		sess := &fakeSession{}
		r, conn := dialServer(t, sess)
		expect(t, r, 220)
		ehlo(t, r, conn)

		// An ASCII sender needs no extension, so the transaction opens normally.
		send(t, conn, "MAIL FROM:<alice@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "RCPT TO:<"+utf8Rcpt+">\r\n")
		expect(t, r, 553)
	})

	t.Run("the keyword permits both", func(t *testing.T) {
		sess := &fakeSession{}
		r, conn := dialServer(t, sess)
		expect(t, r, 220)
		ehlo(t, r, conn)

		send(t, conn, "MAIL FROM:<"+utf8From+"> SMTPUTF8\r\n")
		expect(t, r, 250)
		send(t, conn, "RCPT TO:<"+utf8Rcpt+">\r\n")
		expect(t, r, 250)
		if sess.from != utf8From {
			t.Errorf("UTF-8 sender mangled: from = %q, want %q", sess.from, utf8From)
		}
	})

	t.Run("the keyword does not survive the transaction", func(t *testing.T) {
		sess := &fakeSession{}
		r, conn := dialServer(t, sess)
		expect(t, r, 220)
		ehlo(t, r, conn)

		send(t, conn, "MAIL FROM:<"+utf8From+"> SMTPUTF8\r\n")
		expect(t, r, 250)
		send(t, conn, "RSET\r\n")
		expect(t, r, 250)
		// A second transaction negotiates for itself; the first one's keyword
		// must not carry over.
		send(t, conn, "MAIL FROM:<alice@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "RCPT TO:<"+utf8Rcpt+">\r\n")
		expect(t, r, 553)
	})
}

// ehlo sends EHLO and drains the multiline capability reply, leaving the session
// greeted but with no transaction open.
func ehlo(t *testing.T, r *textproto.Reader, conn net.Conn) {
	t.Helper()
	send(t, conn, "EHLO client.test\r\n")
	if _, _, err := r.ReadResponse(250); err != nil {
		t.Fatalf("EHLO: %v", err)
	}
}
