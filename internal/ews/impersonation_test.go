package ews

import (
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

const impersonationTargetAddr = "boss@hermex.test"

// seededImpersonationEWS builds an EWS server with the caller (alice) and a target
// mailbox. The target holds a uniquely named folder directly under its inbox so an
// operation running against it is distinguishable from the caller's own mailbox.
// When delegated is true the target names the caller a delegate, authorizing
// impersonation; otherwise the caller holds no grant.
func seededImpersonationEWS(t *testing.T, marker string, delegated bool) *httptest.Server {
	t.Helper()
	callerDir := t.TempDir()
	cst, err := objectstore.Open(callerDir)
	if err != nil {
		t.Fatalf("open caller store: %v", err)
	}
	cst.Close()

	targetDir := t.TempDir()
	tst, err := objectstore.Open(targetDir)
	if err != nil {
		t.Fatalf("open target store: %v", err)
	}
	inbox := int64(mapi.PrivateFIDInbox)
	if _, err := tst.CreateFolder(&inbox, marker); err != nil {
		tst.Close()
		t.Fatalf("seed target folder: %v", err)
	}
	if delegated {
		if err := tst.SetDelegates([]string{testUser}); err != nil {
			tst.Close()
			t.Fatalf("seed delegates: %v", err)
		}
	}
	tst.Close()

	accs := directory.StaticAccounts{
		testUser:                {Password: testPass, MailboxPath: callerDir},
		impersonationTargetAddr: {Password: "irrelevant", MailboxPath: targetDir},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

// impersonatedEnvelope wraps an operation in a SOAP envelope carrying an
// ExchangeImpersonation header that names target by its primary SMTP address.
func impersonatedEnvelope(target, innerOp string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="` + nsSOAP + `" xmlns:t="` + nsTypes + `">` +
		`<soap:Header><t:ExchangeImpersonation><t:ConnectingSID>` +
		`<t:PrimarySmtpAddress>` + target + `</t:PrimarySmtpAddress>` +
		`</t:ConnectingSID></t:ExchangeImpersonation></soap:Header>` +
		`<soap:Body>` + innerOp + `</soap:Body></soap:Envelope>`
}

// sidImpersonatedEnvelope is impersonatedEnvelope but with a SID ConnectingSID, the
// unsupported form.
func sidImpersonatedEnvelope(sid, innerOp string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="` + nsSOAP + `" xmlns:t="` + nsTypes + `">` +
		`<soap:Header><t:ExchangeImpersonation><t:ConnectingSID>` +
		`<t:SID>` + sid + `</t:SID>` +
		`</t:ConnectingSID></t:ExchangeImpersonation></soap:Header>` +
		`<soap:Body>` + innerOp + `</soap:Body></soap:Envelope>`
}

// findInboxChildren is the FindFolder Shallow over the inbox used to surface a
// distinguishable child folder of whichever mailbox the session is bound to (the
// proven traversal from folderops_test).
func findInboxChildren() string {
	return `<FindFolder Traversal="Shallow" xmlns="` + nsMessages + `">` +
		`<FolderShape><BaseShape>Default</BaseShape></FolderShape>` +
		`<ParentFolderIds><t:DistinguishedFolderId Id="inbox" xmlns:t="` + nsTypes + `"/></ParentFolderIds>` +
		`</FindFolder>`
}

// TestExchangeImpersonationSwapsMailbox proves an accepted impersonation header
// binds the operation to the TARGET's mailbox: a delegate impersonating the target
// sees the target's uniquely named folder, while the same caller without the header
// sees only their own mailbox (which has no such folder). Asserting no fault would
// prove nothing about whether the mailbox actually changed.
func TestExchangeImpersonationSwapsMailbox(t *testing.T) {
	const marker = "BossSecretRoom"
	ts := seededImpersonationEWS(t, marker, true)

	_, out := soapPost(t, ts, impersonatedEnvelope(impersonationTargetAddr, findInboxChildren()), true)
	if !strings.Contains(out, marker) {
		t.Fatalf("impersonation did not bind to the target mailbox (marker %q absent): %s", marker, out)
	}

	_, ownOut := soapPost(t, ts, wrapRequest(findInboxChildren()), true)
	if strings.Contains(ownOut, marker) {
		t.Fatalf("caller saw the target's folder without impersonation: %s", ownOut)
	}
}

// TestExchangeImpersonationNonDelegateDenied proves a caller the target has NOT
// delegated to is refused. This is the security-critical without-grant path: a gate
// that never denies in tests is an untested gate.
func TestExchangeImpersonationNonDelegateDenied(t *testing.T) {
	ts := seededImpersonationEWS(t, "Secret", false)

	_, out := soapPost(t, ts, impersonatedEnvelope(impersonationTargetAddr, findInboxChildren()), true)
	if !strings.Contains(out, "ErrorImpersonateUserDenied") {
		t.Fatalf("non-delegate impersonation: want ErrorImpersonateUserDenied, got %s", out)
	}
	// And it must NOT have leaked the target's content.
	if strings.Contains(out, "Secret") {
		t.Fatalf("a denied impersonation still exposed the target's folder: %s", out)
	}
}

// TestExchangeImpersonationUnknownTargetDenied proves an unknown target is refused
// with the SAME code as a forbidden one, so the response is not a mailbox-existence
// oracle (OWASP A01).
func TestExchangeImpersonationUnknownTargetDenied(t *testing.T) {
	ts := seededImpersonationEWS(t, "Secret", true)

	_, out := soapPost(t, ts, impersonatedEnvelope("ghost@hermex.test", findInboxChildren()), true)
	if !strings.Contains(out, "ErrorImpersonateUserDenied") {
		t.Fatalf("unknown target: want ErrorImpersonateUserDenied, got %s", out)
	}
}

// TestExchangeImpersonationSIDUnsupported proves a SID-based ConnectingSID fails the
// request (hermEX resolves identities by address, not by Windows SID).
func TestExchangeImpersonationSIDUnsupported(t *testing.T) {
	ts := seededImpersonationEWS(t, "Secret", true)

	_, out := soapPost(t, ts, sidImpersonatedEnvelope("S-1-5-21-1-2-3", findInboxChildren()), true)
	if !strings.Contains(out, "ErrorImpersonationFailed") {
		t.Fatalf("SID impersonation: want ErrorImpersonationFailed, got %s", out)
	}
}

// TestExchangeImpersonationSelfAllowed proves impersonating one's own mailbox is a
// permitted no-op: the operation runs (no fault) against the caller's own store,
// needing no delegate grant.
func TestExchangeImpersonationSelfAllowed(t *testing.T) {
	ts := seededImpersonationEWS(t, "Secret", false)

	_, out := soapPost(t, ts, impersonatedEnvelope(testUser, findInboxChildren()), true)
	if strings.Contains(out, "Fault") || strings.Contains(out, "ErrorImpersonate") {
		t.Fatalf("self-impersonation should be permitted, got %s", out)
	}
	if !strings.Contains(out, "FindFolderResponse") {
		t.Fatalf("self-impersonation did not run the operation: %s", out)
	}
}
