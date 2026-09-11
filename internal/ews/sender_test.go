package ews

import (
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// aliasAccounts is a directory in which the test user also owns a second address, the case
// a client picks a secondary identity from.
type aliasAccounts struct {
	directory.StaticAccounts
	alias string
}

func (a aliasAccounts) Identities(user string) ([]string, error) {
	if !strings.EqualFold(user, testUser) {
		return a.StaticAccounts.Identities(user)
	}
	return []string{testUser, a.alias}, nil
}

// senderServer builds an EWS server whose test user owns the given alias, and returns the
// server plus the mailbox directory.
func senderServer(t *testing.T, alias string) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	accs := aliasAccounts{
		StaticAccounts: directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}},
		alias:          alias,
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

// draftFromRequest saves one draft carrying the given From address.
func draftFromRequest(from string) string {
	return wrapRequest(`<CreateItem MessageDisposition="SaveOnly" xmlns="` + nsMessages + `">` +
		`<Items><t:Message xmlns:t="` + nsTypes + `">` +
		`<t:Subject>identity</t:Subject>` +
		`<t:Body BodyType="Text">body</t:Body>` +
		`<t:From><t:Mailbox><t:EmailAddress>` + from + `</t:EmailAddress></t:Mailbox></t:From>` +
		`<t:ToRecipients><t:Mailbox><t:EmailAddress>` + testUser + `</t:EmailAddress></t:Mailbox></t:ToRecipients>` +
		`</t:Message></Items></CreateItem>`)
}

// draftRaw returns the wire bytes of the single draft in a mailbox.
func draftRaw(t *testing.T, dir string) string {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDDraft))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("drafts = %d, want 1", len(msgs))
	}
	raw, err := st.GetMessageRaw(int64(mapi.PrivateFIDDraft), msgs[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestCreateItemKeepsTheClientsAliasFrom is the load-bearing case: a draft written from a
// secondary address must keep that address, not revert to the primary identity.
func TestCreateItemKeepsTheClientsAliasFrom(t *testing.T) {
	const alias = "sales@hermex.test"
	ts, dir := senderServer(t, alias)

	soapPost(t, ts, draftFromRequest(alias), true)

	if raw := draftRaw(t, dir); !strings.Contains(raw, alias) {
		t.Errorf("the stored draft does not carry the chosen From %q:\n%s", alias, raw)
	}
}

// TestCreateItemRefusesAnUnownedFrom is the security case: a client must not write mail as
// an address it does not hold, so the request is refused and nothing is stored.
func TestCreateItemRefusesAnUnownedFrom(t *testing.T) {
	ts, dir := senderServer(t, "sales@hermex.test")

	_, out := soapPost(t, ts, draftFromRequest("ceo@hermex.test"), true)

	if !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("an unowned From was not refused:\n%s", out)
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if msgs, _ := st.ListMessages(int64(mapi.PrivateFIDDraft)); len(msgs) != 0 {
		t.Errorf("drafts = %d, want 0: a refused identity must store nothing", len(msgs))
	}
}

// TestCreateItemWithoutAFromUsesTheCaller proves an omitted From keeps the previous
// behaviour, so a client that sends none is unaffected.
func TestCreateItemWithoutAFromUsesTheCaller(t *testing.T) {
	ts, dir := senderServer(t, "sales@hermex.test")

	soapPost(t, ts, createItemReq("SaveOnly", testUser, "no from", "body"), true)

	if raw := draftRaw(t, dir); !strings.Contains(raw, testUser) {
		t.Errorf("the stored draft does not carry the caller as From:\n%s", raw)
	}
}

// grantServer builds an EWS server with a second mailbox that granted the test user
// send-as, and returns the server plus the test user's mailbox directory.
func grantServer(t *testing.T, other string) (*httptest.Server, string) {
	return grantingServer(t, other, false)
}

// onBehalfServer is grantServer with a send-on-behalf-of grant instead of send-as.
func onBehalfServer(t *testing.T, other string) (*httptest.Server, string) {
	return grantingServer(t, other, true)
}

// grantingServer builds an EWS server whose second mailbox extends the test user one
// of the two send grants.
func grantingServer(t *testing.T, other string, onBehalf bool) (*httptest.Server, string) {
	t.Helper()
	dir, otherDir := t.TempDir(), t.TempDir()
	st, err := objectstore.Open(otherDir)
	if err != nil {
		t.Fatal(err)
	}
	grant := st.SetSendAs
	if onBehalf {
		grant = st.SetSendOnBehalf
	}
	if err := grant([]string{testUser}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	accs := directory.StaticAccounts{
		testUser: {Password: testPass, MailboxPath: dir},
		other:    {Password: testPass, MailboxPath: otherDir},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

// TestCreateItemAcceptsAGrantedSendAs proves a mailbox that granted the caller send-as can
// be written as, which is the delegate case.
func TestCreateItemAcceptsAGrantedSendAs(t *testing.T) {
	const other = "team@hermex.test"
	ts, dir := grantServer(t, other)

	soapPost(t, ts, draftFromRequest(other), true)

	if raw := draftRaw(t, dir); !strings.Contains(raw, other) {
		t.Errorf("the granted identity did not reach the stored draft:\n%s", raw)
	}
}

// TestGrantedOnBehalfWritesASenderHeader proves the recipient is told who actually wrote
// the message: an on-behalf grant leaves representing and sender different, so oxcmail
// emits a Sender header.
func TestGrantedOnBehalfWritesASenderHeader(t *testing.T) {
	ts, dir := onBehalfServer(t, "desk@hermex.test")

	soapPost(t, ts, draftFromRequest("desk@hermex.test"), true)

	if raw := draftRaw(t, dir); !strings.Contains(raw, "Sender:") {
		t.Errorf("a send-on-behalf carries no Sender header:\n%s", raw)
	}
}

// TestGrantedSendAsWritesNoSenderHeader is the distinction between the two grants. A
// send-as grant says the message is the represented mailbox's own, so naming the caller
// in Sender would disclose a person the grant deliberately keeps off the message.
func TestGrantedSendAsWritesNoSenderHeader(t *testing.T) {
	ts, dir := grantServer(t, "team@hermex.test")

	soapPost(t, ts, draftFromRequest("team@hermex.test"), true)

	if raw := draftRaw(t, dir); strings.Contains(raw, "Sender:") {
		t.Errorf("a send-as carries a Sender header, disclosing the caller:\n%s", raw)
	}
}

// TestCreateItemFromAnAliasWritesNoSenderHeader proves an address the caller owns is sent
// as itself: a Sender header would tell the recipient the message was written on behalf of
// somebody, which is not the case for an alias.
func TestCreateItemFromAnAliasWritesNoSenderHeader(t *testing.T) {
	const alias = "sales@hermex.test"
	ts, dir := senderServer(t, alias)

	soapPost(t, ts, draftFromRequest(alias), true)

	if raw := draftRaw(t, dir); strings.Contains(raw, "Sender:") {
		t.Errorf("an alias send carries a Sender header:\n%s", raw)
	}
}
