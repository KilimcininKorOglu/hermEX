package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
)

// mailqServer builds an admin server over a known temp root so the test can seed
// the relay spool the server reads (its path is fakePaths.RelaySpoolPath()).
func mailqServer(t *testing.T, d Directory) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	srv := NewServer(d, fakePaths{root: root}, []byte("test-secret"))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, root
}

// TestMailQueueManage seeds the outbound relay spool, then drives the admin
// surface against it: the page and JSON list the entry, flush leaves it queued,
// and delete drops it.
func TestMailQueueManage(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts, root := mailqServer(t, d)

	sp, err := relay.Open(filepath.Join(root, "relay.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.Enqueue("boss@local.test", []string{"ext@remote.test"},
		[]byte("Subject: hi\r\n\r\nbody"), time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	entries, _ := sp.List()
	if len(entries) != 1 {
		t.Fatalf("seeded %d entries, want 1", len(entries))
	}
	id := strconv.FormatInt(entries[0].RecipientID, 10)
	sp.Close()

	session, csrf := loginCookies(t, ts)

	// The page lists the queued delivery.
	resp := authedGET(t, ts, "/admin/ui/mailq", session)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "ext@remote.test") {
		t.Fatalf("page (%d) missing the queued recipient:\n%s", resp.StatusCode, body)
	}

	// JSON lists it too.
	resp = authedGET(t, ts, "/admin/mailq", session)
	var got []struct {
		Recipient string `json:"Recipient"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode mailq JSON: %v", err)
	}
	resp.Body.Close()
	if len(got) != 1 || got[0].Recipient != "ext@remote.test" {
		t.Errorf("JSON queue = %+v, want one entry to ext@remote.test", got)
	}

	// Flush leaves the entry queued (it is not delivered, just made due).
	resp = htmxPOST(t, ts, "/admin/ui/mailq/retry", session, csrf, url.Values{"id": {id}})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "ext@remote.test") {
		t.Errorf("flush should leave the entry queued:\n%s", body)
	}

	// Delete drops it; the panel shows the empty state.
	resp = htmxPOST(t, ts, "/admin/ui/mailq/delete", session, csrf, url.Values{"id": {id}})
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "ext@remote.test") {
		t.Errorf("entry still listed after delete:\n%s", body)
	}
	if !strings.Contains(string(body), "queue is empty") {
		t.Errorf("empty-state not shown after deleting the only entry:\n%s", body)
	}
}

// TestMailQueueRequiresSystem proves an org admin cannot reach the mail-queue page.
func TestMailQueueRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminOrg, ScopeID: 1}}}
	ts, _ := mailqServer(t, d)
	session, _ := loginCookies(t, ts)
	resp := authedGET(t, ts, "/admin/ui/mailq", session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-admin mail-queue page = %d, want 403", resp.StatusCode)
	}
}
