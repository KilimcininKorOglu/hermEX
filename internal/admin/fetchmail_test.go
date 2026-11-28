package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

func seedFetchmail(d *fakeDir, mailbox string, entries ...directory.FetchmailEntry) {
	if d.fetchmail == nil {
		d.fetchmail = map[string][]directory.FetchmailEntry{}
	}
	for i := range entries {
		d.nextFMID++
		entries[i].ID = d.nextFMID
		entries[i].Mailbox = mailbox
	}
	d.fetchmail[mailbox] = append(d.fetchmail[mailbox], entries...)
}

// TestAdminListUserFetchmail proves the list is returned and that the stored source
// password is never echoed back.
func TestAdminListUserFetchmail(t *testing.T) {
	d := folderUserDir()
	seedFetchmail(d, "alice@hermex.test", directory.FetchmailEntry{
		SrcServer: "mail.example.com", SrcUser: "alice", SrcPassword: "secret", Protocol: "IMAP", Active: true,
	})
	ts := adminServer(t, d)
	session, _ := loginCookies(t, ts)

	unauth := authedGET(t, ts, "/admin/users/alice@hermex.test/fetchmail", "invalid-session")
	wantStatus(t, unauth, http.StatusUnauthorized, "unauthenticated fetchmail GET")

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/fetchmail", session)
	body := wantBody(t, resp, http.StatusOK, "fetchmail list")
	if !strings.Contains(body, "mail.example.com") {
		t.Errorf("list did not include the entry: %s", body)
	}
	if strings.Contains(body, "secret") {
		t.Errorf("list leaked the source password: %s", body)
	}
}

// TestAdminCreateUserFetchmail proves a valid entry is stored and an invalid protocol is
// refused.
func TestAdminCreateUserFetchmail(t *testing.T) {
	d := folderUserDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := authedPOST(t, ts, "/admin/users/alice@hermex.test/fetchmail", session, csrf,
		`{"srcServer":"mail.example.com","srcUser":"alice","srcPassword":"pw","protocol":"POP3","active":true}`)
	wantStatus(t, resp, http.StatusCreated, "create fetchmail")
	if list := d.fetchmail["alice@hermex.test"]; len(list) != 1 || list[0].SrcServer != "mail.example.com" {
		t.Errorf("entry not stored: %v", d.fetchmail["alice@hermex.test"])
	}

	bad := authedPOST(t, ts, "/admin/users/alice@hermex.test/fetchmail", session, csrf,
		`{"srcServer":"x","srcUser":"y","protocol":"FTP"}`)
	wantStatus(t, bad, http.StatusBadRequest, "bad-protocol status")
}

// TestAdminDeleteUserFetchmail proves an owned entry is deleted and an id belonging to
// another mailbox is refused.
func TestAdminDeleteUserFetchmail(t *testing.T) {
	d := folderUserDir()
	seedFetchmail(d, "alice@hermex.test", directory.FetchmailEntry{SrcServer: "a", SrcUser: "u", Protocol: "POP3"})
	seedFetchmail(d, "bob@hermex.test", directory.FetchmailEntry{SrcServer: "b", SrcUser: "u", Protocol: "POP3"})
	aliceID := d.fetchmail["alice@hermex.test"][0].ID
	bobID := d.fetchmail["bob@hermex.test"][0].ID
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	// Deleting Bob's entry via Alice's URL is refused.
	cross := authedDELETE(t, ts, "/admin/users/alice@hermex.test/fetchmail/"+itoa(bobID), session, csrf, "")
	wantStatus(t, cross, http.StatusNotFound, "cross-user delete status")
	if len(d.fetchmail["bob@hermex.test"]) != 1 {
		t.Error("cross-user delete removed Bob's entry")
	}

	ok := authedDELETE(t, ts, "/admin/users/alice@hermex.test/fetchmail/"+itoa(aliceID), session, csrf, "")
	wantStatus(t, ok, http.StatusNoContent, "delete status")
	if len(d.fetchmail["alice@hermex.test"]) != 0 {
		t.Error("entry not deleted")
	}
}

// TestUIUserAddFetchmail proves the detail-form add stores the entry and returns the
// refreshed panel.
func TestUIUserAddFetchmail(t *testing.T) {
	d := folderUserDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/fetchmail", session, csrf, url.Values{
		"srcServer": {"mail.example.com"},
		"srcUser":   {"alice"},
		"protocol":  {"IMAP"},
		"useSSL":    {"on"},
		"active":    {"on"},
	})
	body := wantBody(t, resp, http.StatusOK, "ui add status")
	if list := d.fetchmail["alice@hermex.test"]; len(list) != 1 || !list[0].UseSSL || !list[0].Active {
		t.Errorf("entry not stored with flags: %v", d.fetchmail["alice@hermex.test"])
	}
	if !strings.Contains(body, "mail.example.com") {
		t.Errorf("refreshed panel missing the entry:\n%s", body)
	}
}

// TestUIUserAddFetchmailRefusesAnUnreadablePort sends a port that is not a number.
// Read as 0 it meant the protocol's default port, so the entry was stored and
// polled a port the operator never entered; now nothing is stored.
func TestUIUserAddFetchmailRefusesAnUnreadablePort(t *testing.T) {
	d := folderUserDir()
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	wantStatus(t, htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/fetchmail", session, csrf, url.Values{
		"srcServer": {"mail.example.com"}, "srcUser": {"alice"}, "protocol": {"IMAP"}, "srcPort": {"99three"},
	}), http.StatusOK, "unreadable port")
	if list := d.fetchmail["alice@hermex.test"]; len(list) != 0 {
		t.Errorf("an entry with an unreadable port was stored: %+v", list)
	}
}

// TestUIUserDeleteFetchmail proves the detail-form delete removes the entry.
func TestUIUserDeleteFetchmail(t *testing.T) {
	d := folderUserDir()
	seedFetchmail(d, "alice@hermex.test", directory.FetchmailEntry{SrcServer: "a", SrcUser: "u", Protocol: "POP3"})
	id := d.fetchmail["alice@hermex.test"][0].ID
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/fetchmail/"+itoa(id)+"/delete", session, csrf, url.Values{})
	wantStatus(t, resp, http.StatusOK, "ui delete status")
	if len(d.fetchmail["alice@hermex.test"]) != 0 {
		t.Error("ui delete did not remove the entry")
	}
}

// TestAdminFetchmailRequiresSystem proves a domain admin cannot list, create or
// delete the fetchmail entries of a user outside its scope through the user-scoped
// API, and cannot use the panel forms, which are system-only.
func TestAdminFetchmailRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}},
		userDetail: directory.UserDetail{Username: "alice@hermex.test"}}
	seedFetchmail(d, "alice@hermex.test", directory.FetchmailEntry{SrcServer: "kept.example.com", SrcUser: "u", Protocol: "POP3"})
	keptID := d.fetchmail["alice@hermex.test"][0].ID
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	// The panel forms are refused too, and neither adds an entry nor removes one.
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/fetchmail", session, csrf, url.Values{
		"srcServer": {"attacker.example.com"}, "srcUser": {"x"}, "protocol": {"IMAP"},
	}), http.StatusForbidden, "domain-admin UI add")
	wantStatus(t, htmxPOST(t, ts, "/admin/ui/users/alice@hermex.test/fetchmail/"+itoa(keptID)+"/delete", session, csrf,
		url.Values{}), http.StatusForbidden, "domain-admin UI delete")
	if list := d.fetchmail["alice@hermex.test"]; len(list) != 1 || list[0].SrcServer != "kept.example.com" {
		t.Errorf("a domain admin changed the fetchmail entries: %+v", list)
	}

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/fetchmail", session)
	wantStatus(t, get, http.StatusForbidden, "domain-admin GET")
	post := authedPOST(t, ts, "/admin/users/alice@hermex.test/fetchmail", session, csrf, `{"srcServer":"x","srcUser":"y","protocol":"POP3"}`)
	wantStatus(t, post, http.StatusForbidden, "domain-admin POST")
	del := authedDELETE(t, ts, "/admin/users/alice@hermex.test/fetchmail/1", session, csrf, "")
	wantStatus(t, del, http.StatusForbidden, "domain-admin DELETE")
}

// TestUIUserDetailShowsFetchmail proves the detail page renders the Fetchmail section with
// a seeded entry and the add form.
func TestUIUserDetailShowsFetchmail(t *testing.T) {
	d := folderUserDir()
	seedFetchmail(d, "alice@hermex.test", directory.FetchmailEntry{
		SrcServer: "remote.example.com", SrcUser: "alice", Protocol: "IMAP", Active: true,
	})
	ts := adminServerStore(t, d, &fakeStore{})
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	body := wantBody(t, resp, http.StatusOK, "detail fetchmail")
	for _, want := range []string{"Fetchmail (remote accounts)", "remote.example.com", `name="srcServer"`} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page fetchmail section missing %q", want)
		}
	}
}
