package webmail

import (
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestMailConversationView is the end-to-end check for the threaded list: with
// conversation view on, a root message and its reply render as one collapsible
// thread group (not two flat rows), and the summary names the count.
func TestMailConversationView(t *testing.T) {
	path := emptyMailbox(t)
	inbox := int64(mapi.PrivateFIDInbox)

	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1700000000, 0)
	if _, err := st.AppendMessage(inbox, []byte("From: a@b.test\r\nMessage-ID: <root@x>\r\nSubject: Project plan\r\n\r\nhi\r\n"), when, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendMessage(inbox, []byte("From: c@b.test\r\nMessage-ID: <re@x>\r\nIn-Reply-To: <root@x>\r\nReferences: <root@x>\r\nSubject: Re: Project plan\r\n\r\nreply\r\n"), when.Add(time.Hour), 0); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadSettings(st)
	cfg.ConversationView = true
	if err := saveSettings(st, cfg); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	_, body := get(t, c, ts.URL+"/mail?folder=INBOX")

	// The thread group wraps the two messages; "2 messages" proves they grouped
	// rather than rendering as two flat rows.
	for _, want := range []string{`class="thread"`, "Project plan", "2 messages"} {
		if !strings.Contains(body, want) {
			t.Errorf("conversation view missing %q", want)
		}
	}
	// The flat bulk toolbar is a flat-view-only feature; it must not appear.
	if strings.Contains(body, `id="bulkform"`) {
		t.Errorf("bulk toolbar should not render in conversation view")
	}
}

// TestMailConversationViewOffStaysFlat confirms the default (setting off) keeps
// the flat list: no thread groups, and the bulk toolbar is present.
func TestMailConversationViewOffStaysFlat(t *testing.T) {
	path := emptyMailbox(t)
	seedMsg(t, path, int64(mapi.PrivateFIDInbox), "Hello", "a@b.test", "body", 1700000000, 0)

	ts := newTestServer(t, path)
	c := authedClient(t, ts)
	_, body := get(t, c, ts.URL+"/mail?folder=INBOX")

	if strings.Contains(body, `class="thread"`) {
		t.Errorf("flat view should render no thread groups")
	}
	if !strings.Contains(body, `id="bulkform"`) {
		t.Errorf("flat view should render the bulk toolbar")
	}
}
