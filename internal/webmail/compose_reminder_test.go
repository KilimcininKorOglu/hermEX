package webmail

import (
	"net/url"
	"strings"
	"testing"
)

// TestComposeAttachmentReminder checks a send whose text mentions an attachment but
// carries none is held for confirmation, then goes through once confirmed.
func TestComposeAttachmentReminder(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// A send mentioning an attachment, with none attached: held with a reminder.
	_, body := postForm(t, c, ts.URL+"/compose", url.Values{
		"action": {"send"}, "to": {"alice@hermex.test"},
		"subject": {"see attached"}, "body": {"the report"},
	})
	if !strings.Contains(body, "mentions an attachment") {
		t.Fatalf("a send without an attachment was not held by the reminder:\n%s", body)
	}
	if !strings.Contains(body, `name="confirmnoattach"`) {
		t.Errorf("the reminder re-render is missing the confirm field")
	}
	if n := len(folderMsgs(t, path, sentFID)); n != 0 {
		t.Fatalf("a held message was sent anyway: Sent has %d, want 0", n)
	}

	// Confirm and resend: it goes through and files a Sent copy.
	postForm(t, c, ts.URL+"/compose", url.Values{
		"action": {"send"}, "to": {"alice@hermex.test"},
		"subject": {"see attached"}, "body": {"the report"}, "confirmnoattach": {"1"},
	})
	if n := len(folderMsgs(t, path, sentFID)); n != 1 {
		t.Errorf("the confirmed send did not file a Sent copy: Sent has %d, want 1", n)
	}
}
