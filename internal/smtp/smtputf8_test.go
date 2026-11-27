package smtp

import (
	"net"
	"net/textproto"
	"strings"
	"testing"
)

// TestServerRefusesANonASCIIAddressWithoutSMTPUTF8 pins RFC 6531 §3.5: the
// extension is negotiated on MAIL, so an address carrying non-ASCII bytes is
// only permitted once the client asked for it.
//
// The reply codes are the ones RFC 6531 §3.5 assigns: 550 on MAIL, 553 on RCPT,
// both with the enhanced code 5.6.7.
func TestServerRefusesANonASCIIAddressWithoutSMTPUTF8(t *testing.T) {
	const utf8From = "posıta@örnek.test"
	const utf8Rcpt = "alıcı@örnek.test"

	t.Run("MAIL without the keyword", func(t *testing.T) {
		for _, addr := range []string{utf8From, "ü@test", "a@örnek.test", "a\x80@test"} {
			sess := &fakeSession{}
			r, conn := dialServer(t, sess)
			expect(t, r, 220)
			ehlo(t, r, conn)

			send(t, conn, "MAIL FROM:<"+addr+">\r\n")
			wantReplyPrefix(t, readCodedReply(t, r, 550, "non-ASCII MAIL"), "5.6.7 ", "MAIL 550 enhanced code")
			wantEq(t, sess.from, "", "sender after refused non-ASCII MAIL")
			send(t, conn, "RCPT TO:<bob@test>\r\n")
			expect(t, r, 503)
		}
	})

	t.Run("RCPT without the keyword", func(t *testing.T) {
		sess := &fakeSession{}
		r, conn := dialServer(t, sess)
		expect(t, r, 220)
		ehlo(t, r, conn)

		send(t, conn, "MAIL FROM:<alice@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "RCPT TO:<"+utf8Rcpt+">\r\n")
		wantReplyPrefix(t, readCodedReply(t, r, 553, "non-ASCII RCPT"), "5.6.7 ", "RCPT 553 enhanced code")
		wantEq(t, len(sess.rcpts), 0, "rcpts after refused non-ASCII RCPT")
		send(t, conn, "DATA\r\n")
		expect(t, r, 503)
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
		wantEq(t, sess.from, utf8From, "UTF-8 sender")
		wantEq(t, len(sess.rcpts), 1, "UTF-8 rcpt count")
		wantEq(t, sess.rcpts[0], utf8Rcpt, "UTF-8 rcpt")
	})

	t.Run("the keyword does not survive the transaction", func(t *testing.T) {
		sess := &fakeSession{}
		r, conn := dialServer(t, sess)
		expect(t, r, 220)
		ehlo(t, r, conn)

		// After RSET.
		send(t, conn, "MAIL FROM:<"+utf8From+"> SMTPUTF8\r\n")
		expect(t, r, 250)
		send(t, conn, "RSET\r\n")
		expect(t, r, 250)
		send(t, conn, "MAIL FROM:<alice@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "RCPT TO:<"+utf8Rcpt+">\r\n")
		expect(t, r, 553)
		wantEq(t, len(sess.rcpts), 0, "rcpts after RSET")

		// After a completed DATA transaction.
		send(t, conn, "MAIL FROM:<"+utf8From+"> SMTPUTF8\r\n")
		expect(t, r, 250)
		send(t, conn, "RCPT TO:<bob@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "DATA\r\n")
		expect(t, r, 354)
		send(t, conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")
		expect(t, r, 250)
		send(t, conn, "MAIL FROM:<alice@test>\r\n")
		expect(t, r, 250)
		send(t, conn, "RCPT TO:<"+utf8Rcpt+">\r\n")
		expect(t, r, 553)
		wantEq(t, len(sess.rcpts), 0, "rcpts after completed DATA")
	})
}

// ehlo sends EHLO and drains the multiline capability reply, leaving the session
// greeted but with no transaction open.
func ehlo(t *testing.T, r *textproto.Reader, conn net.Conn) {
	t.Helper()
	send(t, conn, "EHLO client.test\r\n")
	readCodedReply(t, r, 250, "EHLO")
}

// TestServerRefusesAPathOverTheLimitOrNotUTF8 pins RFC 5321 section 4.5.3.1.3
// (a path is at most 256 octets with its angle brackets) and RFC 6531 section
// 3.3 (an SMTPUTF8 address is UTF-8): such a path is refused with 501 even when
// the client asked for SMTPUTF8, and the refused command records nothing.
func TestServerRefusesAPathOverTheLimitOrNotUTF8(t *testing.T) {
	atLimit := strings.Repeat("a", 254-len("@x.test")) + "@x.test" // 254 + "<>" = 256
	overLimit := "a" + atLimit                                     // 257 octets
	for name, from := range map[string]string{"over the limit": overLimit, "not UTF-8": "a\xff@x.test"} {
		sess := &fakeSession{}
		r, conn := dialServer(t, sess)
		expect(t, r, 220)
		ehlo(t, r, conn)
		send(t, conn, "MAIL FROM:<"+from+"> SMTPUTF8\r\n")
		wantReplyPrefix(t, readCodedReply(t, r, 501, "MAIL "+name), "5.1.7 ", "MAIL "+name+" enhanced code")
		wantEq(t, sess.from, "", "sender after a refused MAIL "+name)

		send(t, conn, "MAIL FROM:<"+atLimit+"> SMTPUTF8\r\n")
		expect(t, r, 250)
		wantEq(t, sess.from, atLimit, "a sender of exactly 256 octets")
		send(t, conn, "RCPT TO:<"+from+">\r\n")
		wantReplyPrefix(t, readCodedReply(t, r, 501, "RCPT "+name), "5.1.3 ", "RCPT "+name+" enhanced code")
		wantEq(t, len(sess.rcpts), 0, "recipients after a refused RCPT "+name)
		send(t, conn, "RCPT TO:<"+atLimit+">\r\n")
		expect(t, r, 250)
		wantEq(t, len(sess.rcpts), 1, "a recipient of exactly 256 octets")
	}
}
