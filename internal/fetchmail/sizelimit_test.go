package fetchmail

import (
	"errors"
	"strings"
	"testing"
)

// allowLoopback lets a test dial its own stub server, which the source-address
// guard refuses by default. It restores the production default afterwards, so a
// test that means to exercise the guard still sees it.
func allowLoopback(t *testing.T) {
	t.Helper()
	SetAllowInternalSources(true)
	t.Cleanup(func() { SetAllowInternalSources(false) })
}

// TestPOP3RetrRefusesAnOversizedMessage proves the RETR reader stops at the cap
// instead of buffering whatever the source sends, and that the session survives:
// the next message still downloads, so one oversized message costs a refusal
// rather than the account's whole poll.
func TestPOP3RetrRefusesAnOversizedMessage(t *testing.T) {
	allowLoopback(t)
	big := "From: a@example.com\r\nSubject: Big\r\n\r\n" + strings.Repeat("x", 4096)
	small := "From: b@example.com\r\nSubject: Small\r\n\r\nfits"
	host, port, _ := fakePOP3(t, []string{big, small})

	SetMaxMessage(1024)
	defer SetMaxMessage(0)

	c, err := dialPOP3(host, port, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.auth("alice", "secret"); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if _, err := c.retr(1); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("retr of an oversized message = %v, want ErrMessageTooLarge", err)
	}
	body, err := c.retr(2)
	if err != nil {
		t.Fatalf("the session did not survive the refusal: %v", err)
	}
	if string(body) != small+"\r\n" {
		t.Errorf("retr(2) = %q, want the second message", body)
	}
}

// TestPOP3RetrAcceptsUpToTheCap is the negative control: a message under the cap
// downloads byte for byte, so the bound refuses only what it should.
func TestPOP3RetrAcceptsUpToTheCap(t *testing.T) {
	allowLoopback(t)
	msg := "From: a@example.com\r\nSubject: Fits\r\n\r\n" + strings.Repeat("y", 900)
	host, port, _ := fakePOP3(t, []string{msg})

	SetMaxMessage(4096)
	defer SetMaxMessage(0)

	c, err := dialPOP3(host, port, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.auth("alice", "secret"); err != nil {
		t.Fatalf("auth: %v", err)
	}
	body, err := c.retr(1)
	if err != nil {
		t.Fatalf("retr under the cap: %v", err)
	}
	if string(body) != msg+"\r\n" {
		t.Errorf("retr = %q, want the message verbatim", body)
	}
}

// TestIMAPFetchRefusesAnOversizedLiteral proves the advertised literal size is
// checked before the allocation. make([]byte, n) on a server-claimed size is the
// whole exposure: the memory is spent whether or not the bytes ever arrive.
func TestIMAPFetchRefusesAnOversizedLiteral(t *testing.T) {
	allowLoopback(t)
	msg := "From: a@example.com\r\nSubject: Big\r\n\r\n" + strings.Repeat("z", 4096)
	host, port, _ := fakeIMAP(t, msg)

	SetMaxMessage(1024)
	defer SetMaxMessage(0)

	c, err := dialIMAP(host, port, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.login("alice", "secret"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := c.selectFolder("INBOX"); err != nil {
		t.Fatalf("select: %v", err)
	}
	if _, err := c.fetchBody("101"); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("fetchBody of an oversized literal = %v, want ErrMessageTooLarge", err)
	}
	// The literal was drained, so the connection is still in step.
	if err := c.logout(); err != nil {
		t.Errorf("the session did not survive the refusal: %v", err)
	}
}

// TestMaxMessageAlwaysHasACeiling proves "no operator limit" still bounds the
// read: the cap exists to stop a remote server from choosing the allocation, so
// 0 and a negative value both fall back to the built-in ceiling.
func TestMaxMessageAlwaysHasACeiling(t *testing.T) {
	defer SetMaxMessage(0)
	for _, n := range []int64{0, -1} {
		SetMaxMessage(n)
		if got := maxMessage(); got != defaultMaxMessage {
			t.Errorf("SetMaxMessage(%d) left the cap at %d, want the built-in %d", n, got, defaultMaxMessage)
		}
	}
	SetMaxMessage(2048)
	if got := maxMessage(); got != 2048 {
		t.Errorf("cap = %d, want the operator's 2048", got)
	}
}

// TestSourceDialRefusesInternalAddressesByDefault proves the worker will not dial
// a source that resolves to an internal address unless a system administrator has
// allowed it. A fetchmail entry is created by a domain-scoped admin, so without
// this the poll is a scheduled dial to any address that role names: this
// deployment's own databases and admin API, or a sweep of the internal network
// read off the failure modes.
func TestSourceDialRefusesInternalAddressesByDefault(t *testing.T) {
	host, port, _ := fakePOP3(t, []string{"From: a@example.com\r\n\r\nbody"})

	SetAllowInternalSources(false) // the production default
	if _, err := dialPOP3(host, port, false, false); err == nil {
		t.Fatal("dialled a loopback source with the block on")
	} else if !strings.Contains(err.Error(), "non-public address") {
		t.Errorf("err = %v, want the address-range refusal", err)
	}

	ihost, iport, _ := fakeIMAP(t, "From: a@example.com\r\n\r\nbody")
	if _, err := dialIMAP(ihost, iport, false, false); err == nil {
		t.Error("dialled a loopback IMAP source with the block on")
	}
}

// TestSourceDialFollowsTheOperatorSwitch is the negative control: an install that
// legitimately fetches from its own private network turns the switch on and the
// same dial succeeds, so the block is a policy rather than a wall.
func TestSourceDialFollowsTheOperatorSwitch(t *testing.T) {
	allowLoopback(t)
	host, port, _ := fakePOP3(t, []string{"From: a@example.com\r\n\r\nbody"})
	c, err := dialPOP3(host, port, false, false)
	if err != nil {
		t.Fatalf("dial with internal sources allowed: %v", err)
	}
	_ = c.quit()
}
