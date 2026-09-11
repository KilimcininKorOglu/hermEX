package webmail2api

import (
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// grantingBox provisions a mailbox that extends alice one of the two send grants.
func grantingBox(t *testing.T, onBehalf bool) string {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	grant := st.SetSendAs
	if onBehalf {
		grant = st.SetSendOnBehalf
	}
	if err := grant([]string{"alice@hermex.test"}); err != nil {
		t.Fatalf("set grant: %v", err)
	}
	return dir
}

// TestResolveSenderAuthorizes proves the send gate: the caller sends as themselves
// freely, as another mailbox only with a positively-confirmed grant, and never as an
// ungranted address. The two grants differ on the wire: a send-as grant names only
// the represented mailbox, an on-behalf grant names the caller in Sender as well.
func TestResolveSenderAuthorizes(t *testing.T) {
	team := grantingBox(t, false) // grants alice send-as
	desk := grantingBox(t, true)  // grants alice send-on-behalf-of
	other := t.TempDir()          // grants nothing
	if st, err := objectstore.Open(other); err == nil {
		st.Close()
	}

	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: t.TempDir()},
		"team@hermex.test":  {Shared: true, MailboxPath: team},
		"desk@hermex.test":  {Shared: true, MailboxPath: desk},
		"other@hermex.test": {Shared: true, MailboxPath: other},
	}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", []byte("s"), "", false)

	cases := []struct {
		name        string
		want        string
		wantRepr    string
		wantSender  string
		wantAllowed bool
	}{
		{"empty is self", "", "alice@hermex.test", "alice@hermex.test", true},
		{"explicit self", "alice@hermex.test", "alice@hermex.test", "alice@hermex.test", true},
		// A send-as grant names ONLY the represented mailbox: keeping the caller in
		// Sender would disclose them on a message the grant says is the mailbox's own.
		{"granted send-as names only the mailbox", "team@hermex.test", "team@hermex.test", "team@hermex.test", true},
		{"granted on-behalf keeps caller in Sender", "desk@hermex.test", "desk@hermex.test", "alice@hermex.test", true},
		{"ungranted mailbox denied", "other@hermex.test", "", "", false},
		{"unknown address denied", "ghost@hermex.test", "", "", false},
	}
	for _, tc := range cases {
		repr, sender, ok := srv.resolveSender("alice@hermex.test", tc.want)
		if ok != tc.wantAllowed {
			t.Errorf("%s: allowed = %v, want %v", tc.name, ok, tc.wantAllowed)
			continue
		}
		if ok && (repr != tc.wantRepr || sender != tc.wantSender) {
			t.Errorf("%s: (repr,sender) = (%q,%q), want (%q,%q)", tc.name, repr, sender, tc.wantRepr, tc.wantSender)
		}
	}
}

// TestBuildOutgoingOnBehalfHeaders proves the wire result: a plain send names
// only From, while a send-on-behalf (representing differs from sender) also emits
// a Sender header naming the real caller, the RFC 5322 "on behalf of" form.
func TestBuildOutgoingOnBehalfHeaders(t *testing.T) {
	accounts := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: t.TempDir()}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", []byte("s"), "", false)
	base := sendRequest{To: []string{"bob@hermex.test"}, Subject: "hi", Body: "b"}

	plain, err := srv.buildOutgoing("alice@hermex.test", "alice@hermex.test", base)
	if err != nil {
		t.Fatalf("plain build: %v", err)
	}
	if h := headerValue(string(plain), "Sender"); h != "" {
		t.Errorf("plain send emitted a Sender header %q, want none", h)
	}
	if h := headerValue(string(plain), "From"); h == "" {
		t.Error("plain send missing From header")
	}

	behalf, err := srv.buildOutgoing("team@hermex.test", "alice@hermex.test", base)
	if err != nil {
		t.Fatalf("on-behalf build: %v", err)
	}
	if h := headerValue(string(behalf), "Sender"); h == "" {
		t.Error("on-behalf send missing Sender header (should name the real caller)")
	}
}

// headerValue returns the first value of a top-level RFC 5322 header, or "".
func headerValue(raw, name string) string {
	prefix := name + ":"
	for line := range strings.SplitSeq(raw, "\r\n") {
		if line == "" {
			break // end of headers
		}
		if len(line) >= len(prefix) && strings.EqualFold(line[:len(prefix)], prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}
