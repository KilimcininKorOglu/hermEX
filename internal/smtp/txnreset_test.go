package smtp

import (
	"net"
	"net/textproto"
	"strings"
	"testing"
)

// A client sends several messages over one connection, and it is not required to
// send RSET between them: RFC 5321 §4.1.1.2 makes a completed DATA end the
// transaction, and §4.1.1.2 makes a new MAIL begin one. The backend session lives
// for the whole connection, so the server has to clear the envelope it holds at
// those boundaries. These tests pin that, because a leaked recipient is delivered
// a message it was never addressed on and the sender is never told.

// sendOneMessage drives one complete transaction and asserts each reply code.
func sendOneMessage(t *testing.T, r *textproto.Reader, conn net.Conn, from, to string) {
	t.Helper()
	send(t, conn, "MAIL FROM:<"+from+">\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<"+to+">\r\n")
	expect(t, r, 250)
	send(t, conn, "DATA\r\n")
	expect(t, r, 354)
	send(t, conn, "Subject: hi\r\n\r\nbody\r\n.\r\n")
	expect(t, r, 250)
}

// wantRecipients fails the test unless the recorded envelope holds exactly the
// expected addresses, in order.
func wantRecipients(t *testing.T, got, want []string, what string) {
	t.Helper()
	wantEq(t, strings.Join(got, ","), strings.Join(want, ","), what)
}

// TestASecondMessageOnOneConnectionCarriesOnlyItsOwnRecipients proves a completed
// DATA clears the backend envelope. Without that the second message keeps the
// first message's recipients, so every one of them is delivered the second message
// as well, and an external one is relayed it.
func TestASecondMessageOnOneConnectionCarriesOnlyItsOwnRecipients(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)

	sendOneMessage(t, r, conn, "alice@test", "bob@test")
	sendOneMessage(t, r, conn, "alice@test", "carol@test")

	if len(sess.dataRcpts) != 2 {
		t.Fatalf("the server accepted %d messages, want 2", len(sess.dataRcpts))
	}
	wantRecipients(t, sess.dataRcpts[0], []string{"bob@test"}, "the first message's recipients")
	wantRecipients(t, sess.dataRcpts[1], []string{"carol@test"}, "the second message's recipients")
	wantEq(t, sess.dataFrom[1], "alice@test", "the second message's sender")
}

// TestANewMailCommandDropsAnAbandonedTransaction proves a MAIL that opens a new
// transaction clears what the abandoned one had routed. A client may restart a
// transaction with MAIL instead of RSET, and the recipients it already named must
// not join the message it then sends.
func TestANewMailCommandDropsAnAbandonedTransaction(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)

	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)
	// The client abandons that transaction by opening another one.
	sendOneMessage(t, r, conn, "alice@test", "carol@test")

	if len(sess.dataRcpts) != 1 {
		t.Fatalf("the server accepted %d messages, want 1", len(sess.dataRcpts))
	}
	wantRecipients(t, sess.dataRcpts[0], []string{"carol@test"}, "the message's recipients")
}
