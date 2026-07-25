package webmail2api

import (
	"testing"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestResolveSenderAuthorizes proves the send-as gate: the caller sends as
// themselves freely, as another mailbox only with a positively-confirmed
// send-as grant (represented with the caller kept in Sender), and never as an
// ungranted address.
func TestResolveSenderAuthorizes(t *testing.T) {
	team := t.TempDir() // a mailbox that grants alice send-as
	st, err := objectstore.Open(team)
	if err != nil {
		t.Fatalf("open team store: %v", err)
	}
	if err := st.SetSendAs([]string{"alice@hermex.test"}); err != nil {
		t.Fatalf("set send-as: %v", err)
	}
	st.Close()

	other := t.TempDir() // a mailbox that grants nothing
	if st2, err := objectstore.Open(other); err == nil {
		st2.Close()
	}

	accounts := directory.StaticAccounts{
		"alice@hermex.test": {MailboxPath: t.TempDir()},
		"team@hermex.test":  {Shared: true, MailboxPath: team},
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
		{"granted send-as keeps caller in Sender", "team@hermex.test", "team@hermex.test", "alice@hermex.test", true},
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
// a Sender header naming the real caller — the RFC 5322 "on behalf of" form.
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
	for _, line := range splitLines(raw) {
		if line == "" {
			break // end of headers
		}
		if len(line) > len(name)+1 && line[len(name)] == ':' &&
			equalFoldASCII(line[:len(name)], name) {
			return line[len(name)+1:]
		}
	}
	return ""
}
