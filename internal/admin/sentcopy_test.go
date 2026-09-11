package admin

import (
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"hermex/internal/objectstore"
)

// TestAdminUserSendOnBehalf proves the on-behalf list is a second, independent list:
// writing it leaves send-as alone, so a mailbox can grant one without the other.
func TestAdminUserSendOnBehalf(t *testing.T) {
	store := &fakeStore{sendAs: map[string][]string{"/mb/alice": {"bob@hermex.test"}}}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/sendonbehalf", session, csrf,
		`["CAROL@Hermex.Test"]`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set send-on-behalf status %d, want 204", resp.StatusCode)
	}
	if store.setSendOnBehalfDir != "/mb/alice" || !slices.Equal(store.setSendOnBehalfVal, []string{"carol@hermex.test"}) {
		t.Errorf("stored on-behalf = %q/%v, want /mb/alice/[carol@hermex.test]",
			store.setSendOnBehalfDir, store.setSendOnBehalfVal)
	}
	if !slices.Equal(store.sendAs["/mb/alice"], []string{"bob@hermex.test"}) {
		t.Errorf("writing the on-behalf list changed send-as to %v", store.sendAs["/mb/alice"])
	}

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/sendonbehalf", session)
	defer get.Body.Close()
	body, _ := io.ReadAll(get.Body)
	if !strings.Contains(string(body), `"data":["carol@hermex.test"]`) {
		t.Errorf("on-behalf body = %s, want the grantee list", body)
	}
}

// TestAdminSetUserSendOnBehalfRejectsUnknown proves the on-behalf list refuses a
// grantee that names no real user, as the send-as list does.
func TestAdminSetUserSendOnBehalfRejectsUnknown(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, knownAlice(), store) // only alice resolves
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/sendonbehalf", session, csrf,
		`["ghost@hermex.test"]`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-grantee status %d, want 404", resp.StatusCode)
	}
	if store.setSendOnBehalfVal != nil {
		t.Errorf("unknown grantee stored as %v, want no store call", store.setSendOnBehalfVal)
	}
}

// TestAdminUserSentCopy proves the two switches round-trip through the API and stay
// two answers: a mailbox may collect what is sent as it without collecting every
// on-behalf send.
func TestAdminUserSentCopy(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	resp := authedPUT(t, ts, "/admin/users/alice@hermex.test/sentcopy", session, csrf,
		`{"forSendAs":true,"forSendOnBehalf":false}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set sent-copy status %d, want 204", resp.StatusCode)
	}
	want := objectstore.SentCopyConfig{ForSendAs: true}
	if store.setSentCopyDir != "/mb/alice" || store.setSentCopyVal != want {
		t.Errorf("stored sent-copy = %q/%+v, want /mb/alice/%+v",
			store.setSentCopyDir, store.setSentCopyVal, want)
	}

	get := authedGET(t, ts, "/admin/users/alice@hermex.test/sentcopy", session)
	defer get.Body.Close()
	body, _ := io.ReadAll(get.Body)
	if !strings.Contains(string(body), `"forSendAs":true`) ||
		!strings.Contains(string(body), `"forSendOnBehalf":false`) {
		t.Errorf("sent-copy body = %s, want the two flags", body)
	}
}

// TestUIUserDetailShowsSendOnBehalfAndSentCopy proves the detail page renders both new
// sections with the stored values populated, so an operator can see a grant is in place.
func TestUIUserDetailShowsSendOnBehalfAndSentCopy(t *testing.T) {
	store := &fakeStore{
		sendOnBehalf: map[string][]string{"/mb/alice": {"carol@hermex.test"}},
		sentCopy:     map[string]objectstore.SentCopyConfig{"/mb/alice": {ForSendOnBehalf: true}},
	}
	ts := adminServerStore(t, folderUserDir(), store)
	session, _ := loginCookies(t, ts)

	resp := authedGET(t, ts, "/admin/ui/users/alice@hermex.test", session)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"<h2>Send on behalf</h2>", `name="sendonbehalf"`, "carol@hermex.test",
		`name="copysendonbehalf" checked`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("detail page missing %q", want)
		}
	}
	// The unset switch must render unchecked, or the page claims a setting nobody made.
	if strings.Contains(string(body), `name="copysendas" checked`) {
		t.Error("the send-as copy switch renders checked although it is off")
	}
}

// TestUIUserSentCopy proves the detail form saves both switches, including turning one
// off: an unchecked box sends no value at all, so a handler that only reads present
// fields would leave the setting on forever.
func TestUIUserSentCopy(t *testing.T) {
	store := &fakeStore{
		sentCopy: map[string]objectstore.SentCopyConfig{"/mb/alice": {ForSendAs: true, ForSendOnBehalf: true}},
	}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/sentcopy", session, csrf, url.Values{
		"copysendonbehalf": {"on"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui sent-copy save status %d, want 200", resp.StatusCode)
	}
	want := objectstore.SentCopyConfig{ForSendOnBehalf: true}
	if store.setSentCopyVal != want {
		t.Errorf("stored sent-copy = %+v, want %+v (the unchecked box turns its setting off)",
			store.setSentCopyVal, want)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("ui sent-copy save did not report success:\n%s", body)
	}
}

// TestUIUserSendOnBehalf proves the detail form writes the on-behalf list, trimmed and
// with blank lines dropped.
func TestUIUserSendOnBehalf(t *testing.T) {
	store := &fakeStore{}
	ts := adminServerStore(t, folderUserDir(), store)
	session, csrf := loginCookies(t, ts)

	resp := htmxPUT(t, ts, "/admin/ui/users/alice@hermex.test/sendonbehalf", session, csrf, url.Values{
		"sendonbehalf": {"bob@hermex.test\n  carol@hermex.test  \n\n"},
	})
	defer resp.Body.Close()
	want := []string{"bob@hermex.test", "carol@hermex.test"}
	if !slices.Equal(store.setSendOnBehalfVal, want) {
		t.Errorf("stored on-behalf = %v, want %v", store.setSendOnBehalfVal, want)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Saved") {
		t.Errorf("ui on-behalf save did not report success:\n%s", body)
	}
}
