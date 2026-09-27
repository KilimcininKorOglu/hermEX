package admin

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
)

// zonedAdmin is a system admin whose stored time zone is Europe/Istanbul.
func zonedAdmin() *fakeDir {
	return &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		uiPrefs: map[string]directory.UserPrefs{"admin@hermex.test": {Timezone: "Europe/Istanbul"}},
	}
}

// TestMobileDevicesShowTheLastActivityAsADistance proves the live monitor shows
// how long ago a session was active, with the exact time in the operator's zone
// in the tooltip, rather than a raw second count.
func TestMobileDevicesShowTheLastActivityAsADistance(t *testing.T) {
	d := zonedAdmin()
	d.activeSessions = []directory.SessionRecord{{ID: "s1", Username: "alice@hermex.test", DeviceID: "dev1",
		LastUpdate: time.Now().Add(-125 * time.Second).Unix()}}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	page := wantBody(t, authedGET(t, ts, "/admin/ui/mobile-devices", session), http.StatusOK, "mobile devices")
	wantContains(t, page, ">2 min ago</time>", "the distance")
	wantContains(t, page, "Europe/Istanbul", "the tooltip names the operator's zone")
}

// TestMailQueueShowsTheNextRetryAsADistance proves a deferred delivery says when
// it is retried as a distance from now.
func TestMailQueueShowsTheNextRetryAsADistance(t *testing.T) {
	ts, root := mailqServer(t, zonedAdmin())
	sp, err := relay.Open(filepath.Join(root, "relay.sqlite3"))
	mustNoErr(t, err, "open the relay spool")
	mustNoErr(t, sp.Enqueue("boss@local.test", []string{"ext@remote.test"},
		[]byte("Subject: hi\r\n\r\nbody"), time.Now().Add(-3*time.Hour)), "enqueue")
	entries, err := sp.List()
	mustNoErr(t, err, "list the spool")
	mustNoErr(t, sp.Retry(entries[0].RecipientID, time.Now().Add(10*time.Minute+30*time.Second), "451 try later"), "defer")
	mustNoErr(t, sp.Close(), "close the spool")

	session, _ := loginCookies(t, ts)
	page := wantBody(t, authedGET(t, ts, "/admin/ui/mailq", session), http.StatusOK, "mail queue")
	wantContains(t, page, ">3 h ago</time>", "the enqueue time")
	wantContains(t, page, ">in 10 min</time>", "the next retry")
}

// TestTLSCertsShowTheDaysLeft proves a stored certificate shows how many days it
// has left, and one close to expiry is flagged.
func TestTLSCertsShowTheDaysLeft(t *testing.T) {
	d := zonedAdmin()
	now := time.Now()
	d.tlsCerts = []directory.TLSCertInfo{
		{Name: "far.test", NotAfter: now.Add(90*24*time.Hour + time.Hour).UnixMilli()},
		{Name: "near.test", NotAfter: now.Add(5*24*time.Hour + time.Hour).UnixMilli()},
		{Name: "old.test", NotAfter: now.Add(-time.Hour).UnixMilli()},
	}
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)
	page := wantBody(t, authedGET(t, ts, "/admin/ui/tls", session), http.StatusOK, "TLS page")
	wantContains(t, page, `<small class="">90 days left</small>`, "a distant expiry is not flagged")
	wantContains(t, page, `<small class="status status-warn">5 days left</small>`, "a near expiry is flagged")
	wantContains(t, page, `<small class="status status-warn">Expired</small>`, "an expired certificate is flagged")
}
