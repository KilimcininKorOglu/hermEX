package webmail

import (
	"net/url"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestMessagePreviewPartialOmitsChrome verifies that mode=preview returns the
// reader content as a bare partial (no page chrome) while the default reader is
// a full HTML document — both carrying the message body and reply controls.
func TestMessagePreviewPartialOmitsChrome(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "Preview subject", "", "preview body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, full := get(t, c, ts.URL+"/message?folder=INBOX&uid="+itoa(uid))
	if !strings.Contains(full, "<!DOCTYPE html>") {
		t.Error("full reader is missing the page chrome (<!DOCTYPE html>)")
	}
	if !strings.Contains(full, "Preview subject") {
		t.Error("full reader is missing the subject")
	}

	_, prev := get(t, c, ts.URL+"/message?folder=INBOX&uid="+itoa(uid)+"&mode=preview")
	if strings.Contains(prev, "<!DOCTYPE html>") {
		t.Error("preview partial leaked the page chrome (<!DOCTYPE html>)")
	}
	if !strings.Contains(prev, "Preview subject") {
		t.Error("preview partial is missing the subject")
	}
	if !strings.Contains(prev, "Reply") {
		t.Error("preview partial is missing the inline Reply control")
	}
	// The "Back to mailbox" link is suppressed in preview (the list is alongside).
	if strings.Contains(prev, "Back to") {
		t.Error("preview partial should not render the Back link")
	}
}

// TestPreviewPaneLayout checks that the reading-pane location drives the split
// layout: right/bottom render the pane container, none renders the bare list.
func TestPreviewPaneLayout(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "msg", "", "body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	_, right := get(t, c, ts.URL+"/mail?folder=INBOX&preview=right")
	if !strings.Contains(right, `id="previewpane"`) || !strings.Contains(right, "preview-right") {
		t.Error("preview=right did not render the reading-pane split")
	}
	_, none := get(t, c, ts.URL+"/mail?folder=INBOX&preview=none")
	if strings.Contains(none, `id="previewpane"`) {
		t.Error("preview=none should not render a reading pane")
	}
}

// TestPreviewPaneDefaultPersists checks that the saved reading-pane location is
// used when no URL param is present, and that a URL param overrides it.
func TestPreviewPaneDefaultPersists(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "msg", "", "body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// Default is "none": a fresh mailbox renders no pane.
	if _, body := get(t, c, ts.URL+"/mail?folder=INBOX"); strings.Contains(body, `id="previewpane"`) {
		t.Fatal("default mailbox should render no reading pane")
	}

	// Persist "bottom" via settings, then the param-less list adopts it.
	postForm(t, c, ts.URL+"/settings", url.Values{"action": {"save"}, "previewpane": {"bottom"}})
	_, saved := get(t, c, ts.URL+"/mail?folder=INBOX")
	if !strings.Contains(saved, "preview-bottom") {
		t.Error("saved reading-pane default 'bottom' was not applied")
	}
	// A URL param still overrides the saved default.
	if _, body := get(t, c, ts.URL+"/mail?folder=INBOX&preview=none"); strings.Contains(body, `id="previewpane"`) {
		t.Error("preview=none param did not override the saved default")
	}
}
