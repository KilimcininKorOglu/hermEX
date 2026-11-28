package ews

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

func syncItemsReq(folder, syncState string, maxChanges int) string {
	body := `<SyncFolderItems xmlns="` + nsMessages + `">` +
		`<ItemShape><BaseShape>Default</BaseShape></ItemShape>` +
		`<SyncFolderId><t:DistinguishedFolderId Id="` + folder + `" xmlns:t="` + nsTypes + `"/></SyncFolderId>`
	if syncState != "" {
		body += `<SyncState>` + syncState + `</SyncState>`
	}
	if maxChanges > 0 {
		body += `<MaxChangesReturned>` + strconv.Itoa(maxChanges) + `</MaxChangesReturned>`
	}
	body += `</SyncFolderItems>`
	return wrapRequest(body)
}

// countChange counts change elements of one kind in the response. The change
// elements live in the types namespace (t:Create/t:Update/t:Delete) inside the
// m:Changes wrapper, the shape EWS clients require to key the change type, so
// the assertions match that exact wire form, not a namespace-naive tag.
func countChange(out, kind string) int {
	return strings.Count(out, "<"+kind+` xmlns="`+nsTypes+`">`)
}

// inboxUIDs opens the store and returns the Inbox message UIDs.
func inboxUIDs(t *testing.T, dir string) (*objectstore.Store, []objectstore.MessageInfo) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		mustCloseStore(t, st)
		t.Fatalf("list: %v", err)
	}
	return st, msgs
}

// TestConcurrentFolderSyncsKeepTheirState proves SyncFolderItems requests for
// different folders that run at once each keep their folder's sync state. The
// mailbox stores every folder's state in one value, so a request that writes
// back the whole copy it read erases a folder another request saved meanwhile,
// and that client's next sync is refused as an unknown sync state.
func TestConcurrentFolderSyncsKeepTheirState(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)
	folders := []string{"inbox", "drafts", "sentitems", "deleteditems", "junkemail", "outbox"}
	var wg sync.WaitGroup
	errs := make(chan error, len(folders))
	for _, f := range folders {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/EWS/Exchange.asmx", strings.NewReader(body))
			if err != nil {
				errs <- err
				return
			}
			req.Header.Set("Content-Type", "text/xml; charset=utf-8")
			req.SetBasicAuth(testUser, testPass)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_, err = io.Copy(io.Discard, resp.Body)
				if cerr := resp.Body.Close(); err == nil {
					err = cerr
				}
			}
			errs <- err
		}(syncItemsReq(f, "", 0))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustCloseStore(t, st)
	state, err := loadState(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Folders) != len(folders) {
		t.Errorf("stored folder states = %d, want one per synced folder (%d)", len(state.Folders), len(folders))
	}
}

// TestSyncFolderItemsPrime confirms an empty SyncState reports every item as a
// Create and returns a fresh SyncState.
func TestSyncFolderItemsPrime(t *testing.T) {
	ts, _ := seededWithMessage(t, plainMessage, plainMessage)
	_, out := soapPost(t, ts, syncItemsReq("inbox", "", 0), true)
	if got := countChange(out, "Create"); got != 2 {
		t.Errorf("prime creates = %d, want 2: %s", got, out)
	}
	if extractSyncState(out) == "" {
		t.Errorf("prime returned no SyncState: %s", out)
	}
}

// TestSyncFolderItemsNoChange confirms a delta sync with no store changes reports
// no changes.
func TestSyncFolderItemsNoChange(t *testing.T) {
	ts, _ := seededWithMessage(t, plainMessage)
	_, out := soapPost(t, ts, syncItemsReq("inbox", "", 0), true)
	token := extractSyncState(out)
	_, out2 := soapPost(t, ts, syncItemsReq("inbox", token, 0), true)
	if countChange(out2, "Create")+countChange(out2, "Update")+countChange(out2, "Delete") > 0 {
		t.Errorf("no-change sync reported changes: %s", out2)
	}
}

// TestSyncFolderItemsDetectsAdd confirms a message added after the prime is
// reported as a Create.
func TestSyncFolderItemsDetectsAdd(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)
	_, out := soapPost(t, ts, syncItemsReq("inbox", "", 0), true)
	token := extractSyncState(out)

	st, _ := inboxUIDs(t, dir)
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(plainMessage), time.Unix(1718300000, 0), 0); err != nil {
		mustCloseStore(t, st)
		t.Fatal(err)
	}
	mustCloseStore(t, st)

	_, out2 := soapPost(t, ts, syncItemsReq("inbox", token, 0), true)
	if got := countChange(out2, "Create"); got != 1 {
		t.Errorf("adds = %d, want 1: %s", got, out2)
	}
}

// TestSyncFolderItemsDetectsFlagChange is the keystone: a read/unread toggle is
// an in-place update that does NOT bump the change number, yet the snapshot diff
// must still report it as an Update.
func TestSyncFolderItemsDetectsFlagChange(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)
	_, out := soapPost(t, ts, syncItemsReq("inbox", "", 0), true)
	token := extractSyncState(out)

	st, msgs := inboxUIDs(t, dir)
	if len(msgs) != 1 {
		mustCloseStore(t, st)
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	if err := st.SetMessageFlags(int64(mapi.PrivateFIDInbox), msgs[0].UID, objectstore.FlagSeen); err != nil {
		mustCloseStore(t, st)
		t.Fatal(err)
	}
	mustCloseStore(t, st)

	_, out2 := soapPost(t, ts, syncItemsReq("inbox", token, 0), true)
	if got := countChange(out2, "Update"); got != 1 {
		t.Errorf("flag-change updates = %d, want 1 (the keystone): %s", got, out2)
	}
}

// TestSyncFolderItemsDetectsDelete confirms a hard-deleted message is reported as
// a Delete (the store keeps no tombstone, so only the snapshot diff catches it).
func TestSyncFolderItemsDetectsDelete(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)
	_, out := soapPost(t, ts, syncItemsReq("inbox", "", 0), true)
	token := extractSyncState(out)

	st, msgs := inboxUIDs(t, dir)
	if err := st.DeleteMessage(int64(mapi.PrivateFIDInbox), msgs[0].UID); err != nil {
		mustCloseStore(t, st)
		t.Fatal(err)
	}
	mustCloseStore(t, st)

	_, out2 := soapPost(t, ts, syncItemsReq("inbox", token, 0), true)
	if got := countChange(out2, "Delete"); got != 1 {
		t.Errorf("deletes = %d, want 1: %s", got, out2)
	}
}

// TestSyncFolderItemsCap confirms MaxChangesReturned caps the batch, sets
// IncludesLastItemInRange false, and the next sync delivers the rest.
func TestSyncFolderItemsCap(t *testing.T) {
	ts, _ := seededWithMessage(t, plainMessage, plainMessage, plainMessage)
	_, out := soapPost(t, ts, syncItemsReq("inbox", "", 2), true)
	if got := countChange(out, "Create"); got != 2 {
		t.Errorf("capped creates = %d, want 2: %s", got, out)
	}
	if !strings.Contains(out, "<IncludesLastItemInRange>false</IncludesLastItemInRange>") {
		t.Errorf("cap should set IncludesLastItemInRange false: %s", out)
	}
	token := extractSyncState(out)
	_, out2 := soapPost(t, ts, syncItemsReq("inbox", token, 2), true)
	if got := countChange(out2, "Create"); got != 1 {
		t.Errorf("second batch creates = %d, want 1: %s", got, out2)
	}
}

// TestSyncFolderItemsStale confirms an unrecognized SyncState is rejected so the
// client re-primes.
func TestSyncFolderItemsStale(t *testing.T) {
	ts, _ := seededWithMessage(t, plainMessage)
	_, out := soapPost(t, ts, syncItemsReq("inbox", "bogus-token", 0), true)
	if !strings.Contains(out, "ErrorInvalidSyncStateData") {
		t.Errorf("stale SyncState not rejected: %s", out)
	}
}
