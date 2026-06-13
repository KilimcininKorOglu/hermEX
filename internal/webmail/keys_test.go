package webmail

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestKeyboardShortcutWiring verifies the pieces the keymap depends on are
// emitted: keys.js is loaded site-wide and each shortcut's target carries its
// data-key marker. The keydown behavior itself is covered by the browser smoke.
func TestKeyboardShortcutWiring(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "msg", "", "body", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	// keys.js is served and binds keydown.
	if code, body := get(t, c, ts.URL+"/static/keys.js"); code != 200 || !strings.Contains(body, "keydown") {
		t.Fatalf("keys.js not served (code=%d) or missing keydown handler", code)
	}

	// The list loads keys.js and marks the per-row read/flag toggles.
	_, list := get(t, c, ts.URL+"/mail?folder=INBOX")
	for _, want := range []string{`/static/keys.js`, `data-key="toggleread"`, `data-key="flagtoggle"`} {
		if !strings.Contains(list, want) {
			t.Errorf("message list missing %q", want)
		}
	}

	// The reader marks Reply All.
	if _, msg := get(t, c, ts.URL+"/message?folder=INBOX&uid="+itoa(uid)); !strings.Contains(msg, `data-key="replyall"`) {
		t.Error("reader missing data-key=replyall")
	}

	// Compose marks Send and Save draft.
	_, compose := get(t, c, ts.URL+"/compose")
	for _, want := range []string{`data-key="send"`, `data-key="savedraft"`} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose missing %q", want)
		}
	}
}
