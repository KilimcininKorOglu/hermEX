package admin

import (
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// TestUIUserDelegates proves the detail-form save writes the delegate list through
// to the mailbox store, trimming each line and dropping blanks, and reports
// success. The list is the same one NSPI serves, so an admin and Outlook manage
// one source — a dropped or mangled entry would desync them.
func TestUIUserDelegates(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{}
	ts := adminServerStore(t, d, store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/delegates", session, csrf, url.Values{
		"delegates": {"boss@hermex.test\n  assistant@hermex.test  \n\n"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui delegates save status %d, want 200", resp.StatusCode)
	}
	if store.setDelegatesDir != "/mb/alice" {
		t.Errorf("SetDelegates maildir = %q, want /mb/alice", store.setDelegatesDir)
	}
	if got := store.setDelegatesVal; !slices.Equal(got, []string{"boss@hermex.test", "assistant@hermex.test"}) {
		t.Errorf("stored delegates = %v, want [boss assistant] (trimmed, blanks dropped)", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("ui delegates save did not report success:\n%s", body)
	}
}

// TestUIUserDetailShowsDelegates proves the detail page renders the delegates
// section populated from the mailbox store, so an admin sees the current list.
func TestUIUserDetailShowsDelegates(t *testing.T) {
	d := oofUserDir()
	store := &fakeStore{delegates: map[string][]string{"/mb/alice": {"boss@hermex.test"}}}
	ts := adminServerStore(t, d, store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"<h2>Delegates</h2>", `name="delegates"`, "boss@hermex.test"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page delegates section missing %q", want)
		}
	}
}
