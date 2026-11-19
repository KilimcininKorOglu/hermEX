package admin

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
)

// TestMailQueueShowsAHeldDelivery seeds a recipient the relay held after a fault
// inside the server. The queue page is the only place it can be seen and acted
// on, so it must say the delivery is held and offer no next retry time (none will
// come); the administrator's retry must release it for delivery.
func TestMailQueueShowsAHeldDelivery(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts, root := mailqServer(t, d)
	spoolPath := filepath.Join(root, "relay.sqlite3")

	sp, err := relay.Open(spoolPath)
	mustNoErr(t, err, "open the relay spool")
	mustNoErr(t, sp.Enqueue("boss@local.test", []string{"ext@remote.test"},
		[]byte("Subject: hi\r\n\r\nbody"), time.Unix(1700000000, 0)), "enqueue")
	entries, _ := sp.List()
	mustNoErr(t, sp.Hold(entries[0].RecipientID, "held after an internal error"), "hold")
	_ = sp.Close()
	id := strconv.FormatInt(entries[0].RecipientID, 10)

	session, csrf := loginCookies(t, ts)
	page := wantBody(t, authedGET(t, ts, "/admin/ui/mailq", session), http.StatusOK, "mail queue page")
	wantContains(t, page, "Held (internal error)", "the page names the held state")
	wantNotContains(t, page, "Next retry", "a held delivery shows no next retry")

	wantBody(t, htmxPOST(t, ts, "/admin/ui/mailq/retry", session, csrf, url.Values{"id": {id}}),
		http.StatusOK, "retry the held delivery")
	sp, err = relay.Open(spoolPath)
	mustNoErr(t, err, "reopen the relay spool")
	defer sp.Close()
	after, _ := sp.List()
	if len(after) != 1 || after[0].Held {
		t.Errorf("queue after retry = %+v, want the delivery released", after)
	}
}
