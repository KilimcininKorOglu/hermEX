package activesync

import (
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

func inboxID() string { return strconv.FormatInt(int64(mapi.PrivateFIDInbox), 10) }

// seedInbox appends n simple messages to the mailbox's Inbox.
func seedInbox(t *testing.T, dir string, n int) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	when := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		raw := fmt.Sprintf("From: sender%d@hermex.test\r\nTo: alice@hermex.test\r\nSubject: Message %d\r\nDate: Mon, 15 Jun 2026 09:00:00 +0000\r\nMessage-ID: <m%d@hermex.test>\r\n\r\nBody %d\r\n", i, i, i, i)
		if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), when, 0); err != nil {
			t.Fatal(err)
		}
	}
}

// syncReq builds a single-collection Sync request for the Inbox.
func syncReq(key, window string, commands ...*wbxml.Node) *wbxml.Node {
	coll := []*wbxml.Node{
		wbxml.Str(wbxml.ASSyncKey, key),
		wbxml.Str(wbxml.ASCollectionID, inboxID()),
	}
	if window != "" {
		coll = append(coll, wbxml.Str(wbxml.ASWindowSize, window))
	}
	if len(commands) > 0 {
		coll = append(coll, wbxml.Elem(wbxml.ASCommands, commands...))
	}
	return wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections, wbxml.Elem(wbxml.ASCollection, coll...)))
}

func respColl(t *testing.T, root *wbxml.Node) *wbxml.Node {
	t.Helper()
	cols := root.Child(wbxml.ASCollections)
	if cols == nil {
		t.Fatal("Sync response has no Collections")
	}
	coll := cols.Child(wbxml.ASCollection)
	if coll == nil {
		t.Fatal("Sync response has no Collection")
	}
	return coll
}

func countCmds(coll *wbxml.Node) (adds, changes, deletes int) {
	cmds := coll.Child(wbxml.ASCommands)
	if cmds == nil {
		return
	}
	for _, c := range cmds.Children {
		switch c.Tag {
		case wbxml.ASAdd:
			adds++
		case wbxml.ASChange:
			changes++
		case wbxml.ASDelete:
			deletes++
		}
	}
	return
}

// firstUID returns the UID of the Inbox's first message.
func firstUID(t *testing.T, dir string) uint32 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil || len(msgs) == 0 {
		t.Fatalf("no inbox messages: %v", err)
	}
	return msgs[0].UID
}

// TestSyncPrime confirms SyncKey 0 issues a fresh key and returns no items.
func TestSyncPrime(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 3)

	_, root := postCommand(t, ts, "Sync", syncReq("0", ""))
	coll := respColl(t, root)
	if coll.ChildText(wbxml.ASStatus) != "1" {
		t.Errorf("status = %q, want 1", coll.ChildText(wbxml.ASStatus))
	}
	if coll.ChildText(wbxml.ASSyncKey) != "1" {
		t.Errorf("sync key = %q, want 1", coll.ChildText(wbxml.ASSyncKey))
	}
	if coll.Child(wbxml.ASCommands) != nil {
		t.Error("prime must not return items")
	}
}

// TestSyncInitialAdds confirms the first real sync streams every message as an
// Add carrying its subject and a MIME body.
func TestSyncInitialAdds(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 3)

	postCommand(t, ts, "Sync", syncReq("0", ""))
	_, root := postCommand(t, ts, "Sync", syncReq("1", ""))
	coll := respColl(t, root)
	if coll.ChildText(wbxml.ASSyncKey) != "2" {
		t.Errorf("sync key = %q, want 2", coll.ChildText(wbxml.ASSyncKey))
	}
	adds, _, _ := countCmds(coll)
	if adds != 3 {
		t.Fatalf("got %d adds, want 3", adds)
	}
	add := coll.Child(wbxml.ASCommands).Children[0]
	data := add.Child(wbxml.ASData)
	if data.ChildText(wbxml.EMSubject) == "" {
		t.Error("Add ApplicationData has no Subject")
	}
	if body := data.Child(wbxml.ABBody); body == nil || body.ChildText(wbxml.ABType) != "4" {
		t.Error("Add ApplicationData has no MIME (type 4) body")
	}
}

// TestSyncFlagChangeDetected is the keystone: a read-state change made directly
// in the store (as IMAP or webmail would) is reported on the next sync via the
// snapshot diff — something the change-number sync cannot see.
func TestSyncFlagChangeDetected(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 2)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", "")) // snapshot now holds both, key 2

	uid := firstUID(t, dir)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageFlags(int64(mapi.PrivateFIDInbox), uid, objectstore.FlagSeen); err != nil {
		t.Fatal(err)
	}
	st.Close()

	_, root := postCommand(t, ts, "Sync", syncReq("2", ""))
	coll := respColl(t, root)
	_, changes, _ := countCmds(coll)
	if changes != 1 {
		t.Fatalf("got %d changes, want 1 (snapshot diff missed the read-flag change)", changes)
	}
	chg := coll.Child(wbxml.ASCommands).Children[0]
	if chg.Child(wbxml.ASData).ChildText(wbxml.EMRead) != "1" {
		t.Error("change did not report Read=1")
	}
}

// TestSyncDeleteDetected confirms a message removed from the store is reported as
// a Delete on the next sync.
func TestSyncDeleteDetected(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 2)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))

	uid := firstUID(t, dir)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteMessage(int64(mapi.PrivateFIDInbox), uid); err != nil {
		t.Fatal(err)
	}
	st.Close()

	_, root := postCommand(t, ts, "Sync", syncReq("2", ""))
	_, _, deletes := countCmds(respColl(t, root))
	if deletes != 1 {
		t.Fatalf("got %d deletes, want 1", deletes)
	}
}

// TestSyncClientChange confirms a client Change (mark read) is applied to the
// store and not echoed back as a server change.
func TestSyncClientChange(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))

	uid := firstUID(t, dir)
	change := wbxml.Elem(wbxml.ASChange,
		wbxml.Str(wbxml.ASServerID, strconv.FormatUint(uint64(uid), 10)),
		wbxml.Elem(wbxml.ASData, wbxml.Str(wbxml.EMRead, "1")))
	_, root := postCommand(t, ts, "Sync", syncReq("2", "", change))

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := st.MessageFlags(int64(mapi.PrivateFIDInbox), uid)
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if flags&objectstore.FlagSeen == 0 {
		t.Error("client Change did not mark the message read in the store")
	}
	if _, changes, _ := countCmds(respColl(t, root)); changes != 0 {
		t.Errorf("server echoed the client's own change back (%d changes)", changes)
	}
}

// TestSyncWindow confirms WindowSize caps a batch and MoreAvailable drives the
// client to fetch the remainder, with the snapshot advancing only for sent items.
func TestSyncWindow(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 3)
	postCommand(t, ts, "Sync", syncReq("0", ""))

	_, root := postCommand(t, ts, "Sync", syncReq("1", "2"))
	coll := respColl(t, root)
	if adds, _, _ := countCmds(coll); adds != 2 {
		t.Fatalf("first window: got %d adds, want 2", adds)
	}
	if coll.Child(wbxml.ASMoreAvailable) == nil {
		t.Error("first window missing MoreAvailable")
	}
	if coll.ChildText(wbxml.ASSyncKey) != "2" {
		t.Errorf("sync key = %q, want 2", coll.ChildText(wbxml.ASSyncKey))
	}

	_, root2 := postCommand(t, ts, "Sync", syncReq("2", "2"))
	coll2 := respColl(t, root2)
	if adds, _, _ := countCmds(coll2); adds != 1 {
		t.Fatalf("second window: got %d adds, want 1", adds)
	}
	if coll2.Child(wbxml.ASMoreAvailable) != nil {
		t.Error("second window should not set MoreAvailable")
	}
}

// TestSyncInvalidKey confirms a stale key forces a re-prime via Status 3 and key 0.
func TestSyncInvalidKey(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))

	_, root := postCommand(t, ts, "Sync", syncReq("99", ""))
	coll := respColl(t, root)
	if coll.ChildText(wbxml.ASStatus) != "3" {
		t.Errorf("status = %q, want 3", coll.ChildText(wbxml.ASStatus))
	}
	if coll.ChildText(wbxml.ASSyncKey) != "0" {
		t.Errorf("sync key = %q, want 0 (re-prime)", coll.ChildText(wbxml.ASSyncKey))
	}
}

// seedInboxHTML appends one multipart/alternative message carrying both a plain and
// an HTML body, so a BodyPreference Type 2 request has an HTML part to return.
func seedInboxHTML(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := "From: s@hermex.test\r\nTo: alice@hermex.test\r\nSubject: HTML msg\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nplain part\r\n" +
		"--b\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>html part</p>\r\n--b--\r\n"
	when := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), when, 0); err != nil {
		t.Fatal(err)
	}
}

// syncReqBodyPref builds a Sync request carrying one BodyPreference (a Type and an
// optional TruncationSize) in the collection Options.
func syncReqBodyPref(key, typ, truncation string) *wbxml.Node {
	bp := []*wbxml.Node{wbxml.Str(wbxml.ABType, typ)}
	if truncation != "" {
		bp = append(bp, wbxml.Str(wbxml.ABTruncationSize, truncation))
	}
	return wbxml.Elem(wbxml.ASSync, wbxml.Elem(wbxml.ASCollections,
		wbxml.Elem(wbxml.ASCollection,
			wbxml.Str(wbxml.ASSyncKey, key),
			wbxml.Str(wbxml.ASCollectionID, inboxID()),
			wbxml.Elem(wbxml.ASOptions, wbxml.Elem(wbxml.ABBodyPreference, bp...)))))
}

// syncedBody primes the Inbox with the given BodyPreference and returns the first
// Add's AirSyncBase Body node.
func syncedBody(t *testing.T, ts *httptest.Server, typ, truncation string) *wbxml.Node {
	t.Helper()
	postCommand(t, ts, "Sync", syncReqBodyPref("0", typ, truncation))
	_, root := postCommand(t, ts, "Sync", syncReqBodyPref("1", typ, truncation))
	cmds := respColl(t, root).Child(wbxml.ASCommands)
	if cmds == nil || len(cmds.Children) == 0 {
		t.Fatal("Sync returned no Add to read the body from")
	}
	return cmds.Children[0].Child(wbxml.ASData).Child(wbxml.ABBody)
}

// TestSyncBodyPreferencePlain confirms a Type 1 BodyPreference serves the plain-text
// body, not the full MIME (MS-ASAIRS §2.2.2.22).
func TestSyncBodyPreferencePlain(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	body := syncedBody(t, ts, "1", "")
	if body.ChildText(wbxml.ABType) != "1" {
		t.Fatalf("body type = %q, want 1 (plain)", body.ChildText(wbxml.ABType))
	}
	if got := string(body.Child(wbxml.ABData).Opaque); !strings.Contains(got, "Body 1") || strings.Contains(got, "Subject:") {
		t.Errorf("plain body = %q, want the text body without MIME headers", got)
	}
}

// TestSyncBodyPreferenceHTML confirms a Type 2 BodyPreference serves the HTML body
// when one exists.
func TestSyncBodyPreferenceHTML(t *testing.T) {
	ts, dir := seededServer(t)
	seedInboxHTML(t, dir)
	body := syncedBody(t, ts, "2", "")
	if body.ChildText(wbxml.ABType) != "2" {
		t.Fatalf("body type = %q, want 2 (HTML)", body.ChildText(wbxml.ABType))
	}
	if got := string(body.Child(wbxml.ABData).Opaque); !strings.Contains(got, "html part") {
		t.Errorf("HTML body = %q, want the HTML part", got)
	}
}

// TestSyncBodyPreferenceHTMLFallback confirms a Type 2 request on a plain-only
// message downgrades to Type 1 rather than returning an empty HTML body.
func TestSyncBodyPreferenceHTMLFallback(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	body := syncedBody(t, ts, "2", "")
	if body.ChildText(wbxml.ABType) != "1" {
		t.Errorf("plain-only message body type = %q, want 1 (downgraded from HTML)", body.ChildText(wbxml.ABType))
	}
}

// TestSyncBodyPreferenceTruncation confirms TruncationSize caps the body and marks
// it Truncated, while EstimatedDataSize still reports the full length.
func TestSyncBodyPreferenceTruncation(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	body := syncedBody(t, ts, "1", "2")
	if body.ChildText(wbxml.ABTruncated) != "1" {
		t.Errorf("Truncated = %q, want 1", body.ChildText(wbxml.ABTruncated))
	}
	if n := len(body.Child(wbxml.ABData).Opaque); n != 2 {
		t.Errorf("truncated body length = %d, want 2", n)
	}
	if sz := body.ChildText(wbxml.ABEstimatedDataSize); sz == "2" || sz == "" {
		t.Errorf("EstimatedDataSize = %q, want the full (untruncated) length", sz)
	}
}
