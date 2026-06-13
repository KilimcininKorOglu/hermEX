package webmail

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// emptyMailbox provisions a fresh mailbox with no messages, for the draft tests
// that assert exact folder counts.
func emptyMailbox(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return path
}

// folderMsgs lists a folder's messages by its fixed id, for count and uid
// assertions.
func folderMsgs(t *testing.T, path string, fid int64) []objectstore.MessageInfo {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(fid)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// msgRaw returns one message's re-synthesized wire form by folder id and uid.
func msgRaw(t *testing.T, path string, fid int64, uid uint32) string {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// postForm posts a form to the server and returns the status and body.
func postForm(t *testing.T, c *http.Client, u string, vals url.Values) (int, string) {
	t.Helper()
	resp, err := c.PostForm(u, vals)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const (
	draftFID = int64(mapi.PrivateFIDDraft)
	sentFID  = int64(mapi.PrivateFIDSentItems)
)

// TestSaveDraftStoresWithoutSending checks that the savedraft action files a
// compose in Drafts without delivering it and without requiring a recipient: an
// empty-To draft is stored, and nothing lands in Sent.
func TestSaveDraftStoresWithoutSending(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	code, body := postForm(t, c, ts.URL+"/compose", url.Values{
		"action":  {"savedraft"},
		"to":      {""},
		"subject": {"unfinished thought"},
		"body":    {"to be continued"},
	})
	if code != 200 {
		t.Fatalf("savedraft = %d", code)
	}
	if !strings.Contains(body, "Draft saved") {
		t.Errorf("savedraft response is missing the confirmation:\n%s", body)
	}

	if n := len(folderMsgs(t, path, draftFID)); n != 1 {
		t.Fatalf("Drafts has %d messages, want 1", n)
	}
	if n := len(folderMsgs(t, path, sentFID)); n != 0 {
		t.Errorf("saving a draft must not file a Sent copy, found %d", n)
	}
	raw := msgRaw(t, path, draftFID, folderMsgs(t, path, draftFID)[0].UID)
	if !strings.Contains(raw, "unfinished thought") || !strings.Contains(raw, "to be continued") {
		t.Errorf("stored draft lost its subject/body:\n%s", raw)
	}
}

// TestEditDraftPrefillIncludingBcc checks that reopening a saved draft prefills
// every editable field — To, Cc, and the blind Bcc — and carries the draft's
// location as hidden fields so a re-save replaces this same draft. The Bcc
// survives because the stored draft retains it (it is stripped only for the
// delivered wire copy, never from the object), so the re-synthesized draft
// re-emits it and the prefill reads it back.
func TestEditDraftPrefillIncludingBcc(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	postForm(t, c, ts.URL+"/compose", url.Values{
		"action":  {"savedraft"},
		"to":      {"bob@example.com"},
		"cc":      {"dave@example.com"},
		"bcc":     {"carol@example.com"},
		"subject": {"three recipients"},
		"body":    {"draft body text"},
	})
	uid := folderMsgs(t, path, draftFID)[0].UID

	_, body := get(t, c, ts.URL+"/compose?action=editdraft&folder=Drafts&uid="+itoa(uid))
	for _, want := range []string{
		"bob@example.com",   // To
		"dave@example.com",  // Cc
		"carol@example.com", // Bcc — the blind recipient must survive and prefill
		"three recipients",  // Subject
		"draft body text",   // body
	} {
		if !strings.Contains(body, want) {
			t.Errorf("editdraft prefill is missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, `name="draftuid" value="`+itoa(uid)+`"`) {
		t.Errorf("editdraft did not carry the draft uid for re-save:\n%s", body)
	}
	if !strings.Contains(body, `name="draftfolder" value="Drafts"`) {
		t.Errorf("editdraft did not carry the draft folder for re-save:\n%s", body)
	}
}

// TestResaveDraftReplaces checks that re-saving an edited draft replaces the
// original rather than accumulating copies: the Drafts count stays at one and
// the uid advances (there is no in-place updater — a re-save deletes the old
// copy and appends a fresh one with a new, never-reused uid).
func TestResaveDraftReplaces(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	postForm(t, c, ts.URL+"/compose", url.Values{
		"action":  {"savedraft"},
		"subject": {"first version"},
		"body":    {"v1"},
	})
	first := folderMsgs(t, path, draftFID)
	if len(first) != 1 {
		t.Fatalf("after first save, Drafts has %d, want 1", len(first))
	}
	u1 := first[0].UID

	postForm(t, c, ts.URL+"/compose", url.Values{
		"action":   {"savedraft"},
		"draftuid": {itoa(u1)},
		"subject":  {"second version"},
		"body":     {"v2"},
	})
	second := folderMsgs(t, path, draftFID)
	if len(second) != 1 {
		t.Fatalf("after re-save, Drafts has %d, want 1 (re-save must replace, not add)", len(second))
	}
	u2 := second[0].UID
	if u2 <= u1 {
		t.Errorf("re-saved draft uid %d did not advance past %d", u2, u1)
	}
	raw := msgRaw(t, path, draftFID, u2)
	if !strings.Contains(raw, "second version") || strings.Contains(raw, "first version") {
		t.Errorf("re-saved draft did not reflect the edit:\n%s", raw)
	}
}

// TestSendFromDraftClearsIt checks that sending an opened draft delivers it,
// files a Sent copy, and removes the source draft — but only after a successful
// send, so the draft is not lost if delivery fails.
func TestSendFromDraftClearsIt(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	postForm(t, c, ts.URL+"/compose", url.Values{
		"action":  {"savedraft"},
		"subject": {"ready to send"},
		"body":    {"final"},
	})
	u1 := folderMsgs(t, path, draftFID)[0].UID

	postForm(t, c, ts.URL+"/compose", url.Values{
		"action":   {"send"},
		"draftuid": {itoa(u1)},
		"to":       {"alice@hermex.test"},
		"subject":  {"ready to send"},
		"body":     {"final"},
	})

	if n := len(folderMsgs(t, path, draftFID)); n != 0 {
		t.Errorf("a sent draft must be removed from Drafts, found %d", n)
	}
	if n := len(folderMsgs(t, path, sentFID)); n != 1 {
		t.Errorf("sending a draft must file exactly one Sent copy, found %d", n)
	}
}

// TestHTMLDraftPreservesMarkup checks that an HTML draft round-trips with its
// markup intact across save, reopen, and re-save. HTML is the default compose
// format, so flattening a draft to plain text on edit would break the main path:
// the bold markup must survive into the editdraft prefill, and a re-save must
// keep the draft as multipart/alternative carrying that markup — with the count
// still one and the uid advanced.
func TestHTMLDraftPreservesMarkup(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	htmlBody := "<p>rich <strong>bold</strong> draft</p>"
	postForm(t, c, ts.URL+"/compose", url.Values{
		"action":   {"savedraft"},
		"subject":  {"formatted draft"},
		"format":   {"html"},
		"body":     {"rich bold draft"},
		"bodyhtml": {htmlBody},
	})
	u1 := folderMsgs(t, path, draftFID)[0].UID

	// Reopening must carry the HTML body into the editor (so it reopens in HTML
	// mode with the markup), not flatten it to plain text. The markup appears
	// escaped inside the hidden bodyhtml textarea.
	_, body := get(t, c, ts.URL+"/compose?action=editdraft&folder=Drafts&uid="+itoa(u1))
	if !strings.Contains(body, `id="formatfield" value="html"`) {
		t.Errorf("HTML draft did not reopen in HTML mode:\n%s", body)
	}
	if !strings.Contains(body, "&lt;strong&gt;bold&lt;/strong&gt;") {
		t.Errorf("editdraft prefill flattened the HTML markup (bold lost):\n%s", body)
	}

	// Re-saving the HTML draft keeps it as multipart/alternative carrying the
	// markup, replacing (not duplicating) the original.
	postForm(t, c, ts.URL+"/compose", url.Values{
		"action":   {"savedraft"},
		"draftuid": {itoa(u1)},
		"subject":  {"formatted draft"},
		"format":   {"html"},
		"body":     {"rich bold draft"},
		"bodyhtml": {htmlBody},
	})
	drafts := folderMsgs(t, path, draftFID)
	if len(drafts) != 1 {
		t.Fatalf("re-saving the HTML draft left %d drafts, want 1", len(drafts))
	}
	u2 := drafts[0].UID
	if u2 <= u1 {
		t.Errorf("re-saved HTML draft uid %d did not advance past %d", u2, u1)
	}
	raw := msgRaw(t, path, draftFID, u2)
	root := mime.ParseStructure([]byte(raw))
	if root.Type != "multipart" || root.Subtype != "alternative" {
		t.Fatalf("re-saved HTML draft is %s/%s, want multipart/alternative:\n%s", root.Type, root.Subtype, raw)
	}
	var htmlPart *mime.Part
	for _, ch := range root.Children {
		if ch.Type == "text" && ch.Subtype == "html" {
			htmlPart = ch
		}
	}
	if htmlPart == nil {
		t.Fatalf("re-saved HTML draft lost its text/html part:\n%s", raw)
	}
	if hc, _ := htmlPart.DecodedContent(); !strings.Contains(string(hc), "<strong>bold</strong>") {
		t.Errorf("re-saved HTML draft lost its markup = %q", hc)
	}
}

// postAutosave posts a savedraft the way the browser's autosave fetch does —
// with Accept: application/json — and returns the status and decoded JSON reply.
func postAutosave(t *testing.T, c *http.Client, u string, vals url.Values) (int, map[string]string) {
	t.Helper()
	req, err := http.NewRequest("POST", u, strings.NewReader(vals.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]string
	json.Unmarshal(b, &m)
	return resp.StatusCode, m
}

// TestAutosaveReturnsJSONAndReplaces checks the autosave contract: a savedraft
// posted with Accept: application/json replies with the new draft uid as JSON
// instead of a rendered page, and a second autosave carrying that uid replaces
// the same draft — the Drafts count stays at one and the uid advances — rather
// than accumulating copies as the user keeps typing.
func TestAutosaveReturnsJSONAndReplaces(t *testing.T) {
	path := emptyMailbox(t)
	ts := newTestServer(t, path)
	c := authedClient(t, ts)

	code, j := postAutosave(t, c, ts.URL+"/compose", url.Values{
		"action":  {"savedraft"},
		"subject": {"typing"},
		"body":    {"a few words"},
	})
	if code != 200 {
		t.Fatalf("autosave = %d", code)
	}
	if j["draftUid"] == "" {
		t.Fatalf("autosave reply carried no draftUid: %v", j)
	}
	if n := len(folderMsgs(t, path, draftFID)); n != 1 {
		t.Fatalf("after first autosave, Drafts has %d, want 1", n)
	}

	code2, j2 := postAutosave(t, c, ts.URL+"/compose", url.Values{
		"action":   {"savedraft"},
		"draftuid": {j["draftUid"]},
		"subject":  {"typing more"},
		"body":     {"a few more words"},
	})
	if code2 != 200 {
		t.Fatalf("second autosave = %d", code2)
	}
	if n := len(folderMsgs(t, path, draftFID)); n != 1 {
		t.Fatalf("second autosave must replace the draft, Drafts has %d, want 1", n)
	}
	if j2["draftUid"] == "" || j2["draftUid"] == j["draftUid"] {
		t.Errorf("second autosave uid %q did not advance from %q", j2["draftUid"], j["draftUid"])
	}
}

// itoa renders a uint32 uid as decimal for URL and form values.
func itoa(uid uint32) string {
	return strconv.FormatUint(uint64(uid), 10)
}
