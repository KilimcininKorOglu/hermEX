package admin

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
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

	resp := authedGET(t, ts, "/admin/users/alice@hermex.test/fetchmail", session)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "mail.example.com") {
		t.Errorf("list did not include the entry: %s", body)
	}
	if strings.Contains(string(body), "secret") {
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
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d, want 201", resp.StatusCode)
	}
	if list := d.fetchmail["alice@hermex.test"]; len(list) != 1 || list[0].SrcServer != "mail.example.com" {
		t.Errorf("entry not stored: %v", d.fetchmail["alice@hermex.test"])
	}

	bad := authedPOST(t, ts, "/admin/users/alice@hermex.test/fetchmail", session, csrf,
		`{"srcServer":"x","srcUser":"y","protocol":"FTP"}`)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("bad-protocol status %d, want 400", bad.StatusCode)
	}
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
	cross.Body.Close()
	if cross.StatusCode != http.StatusNotFound {
		t.Errorf("cross-user delete status %d, want 404", cross.StatusCode)
	}
	if len(d.fetchmail["bob@hermex.test"]) != 1 {
		t.Error("cross-user delete removed Bob's entry")
	}

	ok := authedDELETE(t, ts, "/admin/users/alice@hermex.test/fetchmail/"+itoa(aliceID), session, csrf, "")
	ok.Body.Close()
	if ok.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d, want 204", ok.StatusCode)
	}
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui add status %d, want 200", resp.StatusCode)
	}
	if list := d.fetchmail["alice@hermex.test"]; len(list) != 1 || !list[0].UseSSL || !list[0].Active {
		t.Errorf("entry not stored with flags: %v", d.fetchmail["alice@hermex.test"])
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "mail.example.com") {
		t.Errorf("refreshed panel missing the entry:\n%s", body)
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui delete status %d, want 200", resp.StatusCode)
	}
	if len(d.fetchmail["alice@hermex.test"]) != 0 {
		t.Error("ui delete did not remove the entry")
	}
}

// TestAdminFetchmailRequiresSystem proves a domain admin cannot list, create, or delete.
func TestAdminFetchmailRequiresSystem(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/fetchmail", session)
	get.Body.Close()
	post := authedPOST(t, ts, "/admin/users/alice@hermex.test/fetchmail", session, csrf, `{"srcServer":"x","srcUser":"y","protocol":"POP3"}`)
	post.Body.Close()
	del := authedDELETE(t, ts, "/admin/users/alice@hermex.test/fetchmail/1", session, csrf, "")
	del.Body.Close()
	if get.StatusCode != http.StatusForbidden || post.StatusCode != http.StatusForbidden || del.StatusCode != http.StatusForbidden {
		t.Errorf("domain-admin fetchmail = GET %d / POST %d / DELETE %d, want all 403", get.StatusCode, post.StatusCode, del.StatusCode)
	}
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
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"Fetchmail (remote accounts)", "remote.example.com", `name="srcServer"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page fetchmail section missing %q", want)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
