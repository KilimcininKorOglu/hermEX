package smtp

import (
	"net"
	"net/textproto"
	"strings"
	"testing"
)

// A client sends several messages over one connection, and it is not required to
// send RSET between them: RFC 5321 §4.1.1.2 makes a completed DATA end the
// transaction, and §4.1.1.2 makes a new MAIL begin one.

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

func sendOneBDATMessage(t *testing.T, r *textproto.Reader, conn net.Conn, from, to string) {
	t.Helper()
	send(t, conn, "MAIL FROM:<"+from+">\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<"+to+">\r\n")
	expect(t, r, 250)
	send(t, conn, "BDAT 4 LAST\r\nbody")
	expect(t, r, 250)
}

// wantRecipients fails the test unless the recorded envelope holds exactly the
// expected addresses, in order.
func wantRecipients(t *testing.T, got, want []string, what string) {
	t.Helper()
	wantEq(t, strings.Join(got, ","), strings.Join(want, ","), what)
}

// TestASecondMessageOnOneConnectionCarriesOnlyItsOwnRecipients proves a completed
// DATA clears the backend envelope.
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
// transaction clears what the abandoned one had routed.
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
	sendOneMessage(t, r, conn, "alice@test", "carol@test")

	if len(sess.dataRcpts) != 1 {
		t.Fatalf("the server accepted %d messages, want 1", len(sess.dataRcpts))
	}
	wantRecipients(t, sess.dataRcpts[0], []string{"carol@test"}, "the message's recipients")
}

// TestRSETAndRefusedMailDoNotLeakRecipients verifies RSET, HELO re-greeting,
// BDAT completion, and refused MAIL commands leave no stale envelope state.
func TestRSETAndRefusedMailDoNotLeakRecipients(t *testing.T) {
	sess := &fakeSession{}
	r, conn := dialServer(t, sess)
	expect(t, r, 220)
	send(t, conn, "EHLO client.test\r\n")
	expect(t, r, 250)

	// RSET clears prior MAIL + RCPT.
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RSET\r\n")
	expect(t, r, 250)
	sendOneMessage(t, r, conn, "alice@test", "carol@test")

	// HELO re-greeting clears prior MAIL + RCPT.
	send(t, conn, "MAIL FROM:<alice@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "RCPT TO:<bob@test>\r\n")
	expect(t, r, 250)
	send(t, conn, "HELO client.test\r\n")
	expect(t, r, 250)
	sendOneMessage(t, r, conn, "alice@test", "dave@test")

	// Refused MAIL does not open a transaction.
	send(t, conn, "MAIL FROM:<alice@test> MT-PRIORITY=99\r\n")
	expect(t, r, 501)
	wantEq(t, sess.from, "", "sender after refused MAIL")
	send(t, conn, "RCPT TO:<eve@test>\r\n")
	expect(t, r, 503)

	// Consecutive BDAT transactions stay isolated.
	sendOneBDATMessage(t, r, conn, "alice@test", "frank@test")
	sendOneBDATMessage(t, r, conn, "alice@test", "grace@test")

	wantEq(t, len(sess.dataRcpts), 4, "total accepted messages")
	wantRecipients(t, sess.dataRcpts[0], []string{"carol@test"}, "post-RSET recipients")
	wantRecipients(t, sess.dataRcpts[1], []string{"dave@test"}, "post-HELO recipients")
	wantRecipients(t, sess.dataRcpts[2], []string{"frank@test"}, "first BDAT recipients")
	wantRecipients(t, sess.dataRcpts[3], []string{"grace@test"}, "second BDAT recipients")
}
