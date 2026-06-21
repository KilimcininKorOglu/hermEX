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

// authedMailqDo issues a state-changing JSON request (session + double-submit
// CSRF) against the mail-queue API and returns the status code.
func authedMailqDo(t *testing.T, ts *httptest.Server, method, path, session, csrf string) int {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	req.Header.Set(csrfHeader, csrf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestMailQueueJSONMutations covers the JSON mutation routes directly (the UI
// routes share the same seam but are exercised separately): a flush flushes the
// entry to immediate retry without dropping it, and a delete removes it. Both
// answer 204 and require the double-submit CSRF token.
func TestMailQueueJSONMutations(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts, root := mailqServer(t, d)
	spoolPath := filepath.Join(root, "relay.sqlite3")

	sp, err := relay.Open(spoolPath)
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

	// A flush without the CSRF header is refused (state-changing route).
	noCSRF, _ := http.NewRequest("POST", ts.URL+"/admin/mailq/"+id+"/retry", nil)
	noCSRF.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	noCSRF.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	if resp, err := http.DefaultClient.Do(noCSRF); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("flush without CSRF = %d, want 403", resp.StatusCode)
		}
	}

	// Flush answers 204 and leaves the entry in the queue.
	if code := authedMailqDo(t, ts, "POST", "/admin/mailq/"+id+"/retry", session, csrf); code != http.StatusNoContent {
		t.Errorf("JSON flush = %d, want 204", code)
	}
	sp, _ = relay.Open(spoolPath)
	if entries, _ := sp.List(); len(entries) != 1 {
		t.Errorf("after flush %d entries, want 1 (flush must not drop)", len(entries))
	}
	sp.Close()

	// Delete answers 204 and removes the entry.
	if code := authedMailqDo(t, ts, "DELETE", "/admin/mailq/"+id, session, csrf); code != http.StatusNoContent {
		t.Errorf("JSON delete = %d, want 204", code)
	}
	sp, _ = relay.Open(spoolPath)
	if entries, _ := sp.List(); len(entries) != 0 {
		t.Errorf("after delete %d entries, want 0", len(entries))
	}
	sp.Close()
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
