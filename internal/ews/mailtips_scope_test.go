package ews

import (
	"net/http/httptest"
	"testing"

	"hermex/internal/directory"
)

// mailTipsForeignServer serves a caller and a recipient that sit in different
// domains, which is what an out-of-scope pair looks like to a static directory.
func mailTipsForeignServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	callerDir, foreignDir := t.TempDir(), t.TempDir()
	accs := directory.StaticAccounts{
		testUser:         {Password: testPass, MailboxPath: callerDir},
		"ceo@other.test": {Password: "ceosecret", MailboxPath: foreignDir},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, foreignDir
}

// TestMailTipsWithholdsOutOfScopeOOF is the disclosure defect. The handler used
// to ignore the session and resolve any mailbox in the deployment, so a caller in
// one organization could read another organization's internal out-of-office text,
// which routinely carries same-organization-only detail. Out of scope must answer
// exactly like an unknown recipient, which also keeps the response from being an
// existence or away-state oracle.
func TestMailTipsWithholdsOutOfScopeOOF(t *testing.T) {
	ts, foreignDir := mailTipsForeignServer(t)
	seedOOF(t, foreignDir, "Back on Monday, reach my deputy on extension 4021.")

	p := getMailTips(t, ts, "ceo@other.test")
	if len(p.Messages) != 1 {
		t.Fatalf("got %d response messages, want 1", len(p.Messages))
	}
	if p.Messages[0].OOFMessage != "" {
		t.Errorf("out-of-scope recipient disclosed %q, want no out-of-office tip", p.Messages[0].OOFMessage)
	}
}

// TestMailTipsKeepsInScopeOOF is the control: the scope check must not break the
// ordinary same-organization tip the feature exists to serve.
func TestMailTipsKeepsInScopeOOF(t *testing.T) {
	ts, bobDir := oofTwoAccountServer(t)
	seedOOF(t, bobDir, "Bob is away until Monday.")

	p := getMailTips(t, ts, "bob@hermex.test")
	if len(p.Messages) != 1 {
		t.Fatalf("got %d response messages, want 1", len(p.Messages))
	}
	if p.Messages[0].OOFMessage != "Bob is away until Monday." {
		t.Errorf("in-scope OutOfOffice message = %q, want Bob's reply", p.Messages[0].OOFMessage)
	}
}

// TestMailTipsFailsClosedWithoutScopeModel proves the refusal direction: a
// directory that cannot answer the scope question yields no tip rather than the
// whole deployment's out-of-office text.
func TestMailTipsFailsClosedWithoutScopeModel(t *testing.T) {
	callerDir, bobDir := t.TempDir(), t.TempDir()
	static := directory.StaticAccounts{
		testUser:          {Password: testPass, MailboxPath: callerDir},
		"bob@hermex.test": {Password: "bobsecret", MailboxPath: bobDir},
	}
	seedOOF(t, bobDir, "Bob is away until Monday.")
	// noScope resolves like the static directory but deliberately offers no
	// ScopeChecker, so the type assertion in the handler fails.
	noScope := struct{ directory.Accounts }{static}
	ts := httptest.NewServer(NewServer(static, noScope, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)

	p := getMailTips(t, ts, "bob@hermex.test")
	if len(p.Messages) != 1 {
		t.Fatalf("got %d response messages, want 1", len(p.Messages))
	}
	if p.Messages[0].OOFMessage != "" {
		t.Errorf("a directory with no scope model disclosed %q, want no tip", p.Messages[0].OOFMessage)
	}
}
