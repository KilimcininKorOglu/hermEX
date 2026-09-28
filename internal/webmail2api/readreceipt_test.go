package webmail2api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// seedReceiptMail files a message from bob in alice's Inbox that asks for a read
// receipt and returns its webmail id.
func seedReceiptMail(t *testing.T, alice string) string {
	t.Helper()
	st := openMailbox(t, alice)
	raw := "From: Bob <bob@hermex.test>\r\nTo: alice@hermex.test\r\nSubject: Did you read this\r\n" +
		"Message-ID: <rr-1@hermex.test>\r\nDisposition-Notification-To: bob@hermex.test\r\n\r\nplease confirm\r\n"
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	mustNoErr(t, "seed the message", err)
	return "inbox:" + strconv.FormatUint(uint64(info.UID), 10)
}

// setReceiptResponse stores alice's webmail read-receipt response.
func setReceiptResponse(t *testing.T, alice string, resp objectstore.ReadReceiptResponse) {
	t.Helper()
	mustNoErr(t, "store the setting", openMailbox(t, alice).SetReadReceiptConfig(objectstore.ReadReceiptConfig{Response: resp}))
}

// receiptsTo counts the read receipts in bob's Inbox.
func receiptsTo(t *testing.T, bob string) int {
	t.Helper()
	return len(folderMail(t, bob, int64(mapi.PrivateFIDInbox)))
}

// TestReadReceiptSettingsRoundTrip proves a fresh mailbox asks, that a saved
// setting reads back and is what the store holds, and that an unknown response is
// refused rather than stored.
func TestReadReceiptSettingsRoundTrip(t *testing.T) {
	do, alice, _ := meetingHarness(t)
	got := okBody[readReceiptSettingsJSON](t, "get", do(http.MethodGet, "/api/v1/settings/read-receipts", ""))
	wantEq(t, "the default", got, readReceiptSettingsJSON{Response: "ask"})

	okBody[readReceiptSettingsJSON](t, "put", do(http.MethodPut, "/api/v1/settings/read-receipts", `{"response":"always","suppressActiveSync":true}`))
	got = okBody[readReceiptSettingsJSON](t, "get", do(http.MethodGet, "/api/v1/settings/read-receipts", ""))
	wantEq(t, "after the PUT", got, readReceiptSettingsJSON{Response: "always", SuppressActiveSync: true})
	cfg, err := openMailbox(t, alice).GetReadReceiptConfig()
	mustNoErr(t, "read the stored setting", err)
	wantEq(t, "the stored setting", cfg, objectstore.ReadReceiptConfig{Response: objectstore.ReadReceiptAlways, SuppressActiveSync: true})

	wantStatus(t, "an unknown response", do(http.MethodPut, "/api/v1/settings/read-receipts", `{"response":"sometimes"}`), http.StatusBadRequest)
}

// TestReadReceiptAskPromptsAndSends proves "ask" sends nothing on open but reports
// the pending receipt, that agreeing sends it once, and that the prompt is gone
// afterwards.
func TestReadReceiptAskPromptsAndSends(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := seedReceiptMail(t, alice)

	d := okBody[mailDetailJSON](t, "open", do(http.MethodGet, "/api/v1/mail/message?id="+id, ""))
	wantEq(t, "the prompt", d.ReceiptRequested, true)
	wantEq(t, "receipts before answering", receiptsTo(t, bob), 0)

	wantStatus(t, "send", do(http.MethodPost, "/api/v1/mail/read-receipt", `{"id":"`+id+`","send":true}`), http.StatusOK)
	wantEq(t, "receipts after agreeing", receiptsTo(t, bob), 1)
	wantContains(t, "the receipt", folderMail(t, bob, int64(mapi.PrivateFIDInbox))[0], "manual-action/MDN-sent-manually")
	d = okBody[mailDetailJSON](t, "reopen", do(http.MethodGet, "/api/v1/mail/message?id="+id, ""))
	wantEq(t, "the prompt after sending", d.ReceiptRequested, false)
}

// TestReadReceiptAskDecline proves declining sends nothing and ends the prompt.
func TestReadReceiptAskDecline(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	id := seedReceiptMail(t, alice)

	wantStatus(t, "decline", do(http.MethodPost, "/api/v1/mail/read-receipt", `{"id":"`+id+`","send":false}`), http.StatusOK)
	d := okBody[mailDetailJSON](t, "open", do(http.MethodGet, "/api/v1/mail/message?id="+id, ""))
	wantEq(t, "the prompt after declining", d.ReceiptRequested, false)
	wantEq(t, "receipts after declining", receiptsTo(t, bob), 0)
}

// TestReadReceiptAlwaysAndNever proves "always" sends on open without a prompt and
// "never" neither sends nor prompts.
func TestReadReceiptAlwaysAndNever(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	setReceiptResponse(t, alice, objectstore.ReadReceiptAlways)
	d := okBody[mailDetailJSON](t, "open", do(http.MethodGet, "/api/v1/mail/message?id="+seedReceiptMail(t, alice), ""))
	wantEq(t, "the prompt under always", d.ReceiptRequested, false)
	wantEq(t, "receipts under always", receiptsTo(t, bob), 1)

	do, alice, bob = meetingHarness(t)
	setReceiptResponse(t, alice, objectstore.ReadReceiptNever)
	d = okBody[mailDetailJSON](t, "open", do(http.MethodGet, "/api/v1/mail/message?id="+seedReceiptMail(t, alice), ""))
	wantEq(t, "the prompt under never", d.ReceiptRequested, false)
	wantEq(t, "receipts under never", receiptsTo(t, bob), 0)
}

// TestReadReceiptAlwaysOnMarkRead proves "always" also sends when the message is
// marked read from the list, one at a time or the whole folder.
func TestReadReceiptAlwaysOnMarkRead(t *testing.T) {
	do, alice, bob := meetingHarness(t)
	setReceiptResponse(t, alice, objectstore.ReadReceiptAlways)
	id := seedReceiptMail(t, alice)
	wantStatus(t, "mark read", do(http.MethodPost, "/api/v1/mail/flag", `{"id":"`+id+`","flag":"\\Seen","value":true}`), http.StatusOK)
	wantEq(t, "receipts after marking read", receiptsTo(t, bob), 1)

	do, alice, bob = meetingHarness(t)
	setReceiptResponse(t, alice, objectstore.ReadReceiptAlways)
	seedReceiptMail(t, alice)
	wantStatus(t, "mark all read", do(http.MethodPost, "/api/v1/mail/mark-all-read", `{"folder":"inbox"}`), http.StatusOK)
	wantEq(t, "receipts after marking the folder read", receiptsTo(t, bob), 1)
}
