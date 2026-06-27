package ews

import (
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/objectstore"
)

// mailTipsBody builds a GetMailTips request for the given recipient addresses.
func mailTipsBody(addrs ...string) string {
	var rc strings.Builder
	for _, a := range addrs {
		rc.WriteString(`<t:Mailbox><t:EmailAddress>`)
		rc.WriteString(a)
		rc.WriteString(`</t:EmailAddress><t:RoutingType>SMTP</t:RoutingType></t:Mailbox>`)
	}
	return `<GetMailTips xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<SendingAs><t:EmailAddress>` + testUser + `</t:EmailAddress></SendingAs>` +
		`<Recipients>` + rc.String() + `</Recipients>` +
		`<MailTipsRequested>All</MailTipsRequested>` +
		`</GetMailTips>`
}

// parsedMailTips reads the per-recipient tips from a GetMailTips response.
type parsedMailTips struct {
	Messages []struct {
		Address    string `xml:"MailTips>RecipientAddress>EmailAddress"`
		OOFMessage string `xml:"MailTips>OutOfOffice>ReplyBody>Message"`
	} `xml:"Body>GetMailTipsResponse>ResponseMessages>MailTipsResponseMessageType"`
}

func getMailTips(t *testing.T, ts *httptest.Server, addrs ...string) parsedMailTips {
	t.Helper()
	_, body := soapPost(t, ts, wrapRequest(mailTipsBody(addrs...)), true)
	var p parsedMailTips
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse GetMailTips response: %v\n%s", err, body)
	}
	return p
}

// seedOOF enables a mailbox's auto-reply with the given internal reply.
func seedOOF(t *testing.T, dir, internalReply string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetOOFSettings(objectstore.OOFSettings{Enabled: true, InternalReply: internalReply}); err != nil {
		t.Fatal(err)
	}
}

// TestMailTipsOutOfOffice proves a recipient with an active auto-reply surfaces
// the out-of-office tip carrying that recipient's internal reply.
func TestMailTipsOutOfOffice(t *testing.T) {
	ts, bobDir := oofTwoAccountServer(t)
	seedOOF(t, bobDir, "Bob is away until Monday.")

	p := getMailTips(t, ts, "bob@hermex.test")
	if len(p.Messages) != 1 {
		t.Fatalf("got %d response messages, want 1", len(p.Messages))
	}
	if p.Messages[0].Address != "bob@hermex.test" {
		t.Errorf("RecipientAddress = %q, want bob@hermex.test", p.Messages[0].Address)
	}
	if p.Messages[0].OOFMessage != "Bob is away until Monday." {
		t.Errorf("OutOfOffice message = %q, want Bob's reply", p.Messages[0].OOFMessage)
	}
}

// TestMailTipsNotAway proves a recipient whose auto-reply is off carries no
// out-of-office tip.
func TestMailTipsNotAway(t *testing.T) {
	ts, _ := oofTwoAccountServer(t) // bob's store has no OOF set

	p := getMailTips(t, ts, "bob@hermex.test")
	if len(p.Messages) != 1 {
		t.Fatalf("got %d response messages, want 1", len(p.Messages))
	}
	if p.Messages[0].OOFMessage != "" {
		t.Errorf("OutOfOffice message = %q, want empty (not away)", p.Messages[0].OOFMessage)
	}
}

// TestMailTipsUnknownRecipient proves a non-local address (a valid external
// recipient is indistinguishable from a typo) yields a tip entry with no
// out-of-office, rather than an error.
func TestMailTipsUnknownRecipient(t *testing.T) {
	ts, _ := oofTwoAccountServer(t)

	p := getMailTips(t, ts, "stranger@elsewhere.test")
	if len(p.Messages) != 1 {
		t.Fatalf("got %d response messages, want 1", len(p.Messages))
	}
	if p.Messages[0].Address != "stranger@elsewhere.test" {
		t.Errorf("RecipientAddress = %q, want the echoed external address", p.Messages[0].Address)
	}
	if p.Messages[0].OOFMessage != "" {
		t.Errorf("OutOfOffice message = %q, want empty for a non-local recipient", p.Messages[0].OOFMessage)
	}
}

// TestMailTipsMultipleRecipients proves one response message is returned per
// recipient, each matched to its own out-of-office state.
func TestMailTipsMultipleRecipients(t *testing.T) {
	ts, bobDir := oofTwoAccountServer(t)
	seedOOF(t, bobDir, "Bob is away.")

	p := getMailTips(t, ts, "bob@hermex.test", "stranger@elsewhere.test")
	if len(p.Messages) != 2 {
		t.Fatalf("got %d response messages, want 2", len(p.Messages))
	}
	byAddr := map[string]string{}
	for _, m := range p.Messages {
		byAddr[m.Address] = m.OOFMessage
	}
	if byAddr["bob@hermex.test"] != "Bob is away." {
		t.Errorf("bob OOF = %q, want Bob's reply", byAddr["bob@hermex.test"])
	}
	if byAddr["stranger@elsewhere.test"] != "" {
		t.Errorf("stranger OOF = %q, want empty", byAddr["stranger@elsewhere.test"])
	}
}
