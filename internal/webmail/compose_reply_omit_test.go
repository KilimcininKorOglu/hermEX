package webmail

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestReplyOmitOriginal checks the "do not quote the original when replying"
// setting: a reply prefill quotes the original by default and omits it when set.
func TestReplyOmitOriginal(t *testing.T) {
	path := emptyMailbox(t)
	uid := seedMsg(t, path, int64(mapi.PrivateFIDInbox), "hi", "bob@hermex.test", "ORIGINALBODYTEXT", 100, 0)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	if _, page := get(t, c, ts.URL+"/compose?action=reply&folder=INBOX&uid="+itoa(uid)); !strings.Contains(page, "ORIGINALBODYTEXT") {
		t.Errorf("default reply did not quote the original body")
	}

	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadSettings(st)
	if err != nil {
		t.Fatal(err)
	}
	cfg.OmitOriginalOnReply = true
	if err := saveSettings(st, cfg); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if _, page := get(t, c, ts.URL+"/compose?action=reply&folder=INBOX&uid="+itoa(uid)); strings.Contains(page, "ORIGINALBODYTEXT") {
		t.Errorf("with omit-original on, the reply still quoted the original:\n%s", page)
	}
}
