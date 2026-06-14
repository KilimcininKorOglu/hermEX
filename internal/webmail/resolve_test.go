package webmail

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"hermex/internal/directory"
)

// newGALServer builds a webmail server over a multi-user static directory, so
// the GAL search has more than one address to match. The accounts' mailbox paths
// are dummies: /resolve never opens a mailbox, and login only reads the path.
func newGALServer(t *testing.T) *httptest.Server {
	t.Helper()
	accts := directory.StaticAccounts{
		"alice@hermex.test":  {Password: "secret", MailboxPath: "/m/alice"},
		"albert@hermex.test": {Password: "x", MailboxPath: "/m/albert"},
		"bob@hermex.test":    {Password: "x", MailboxPath: "/m/bob"},
	}
	srv, err := NewServer(accts, accts, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestResolveRequiresSession locks the auth gate: an unauthenticated caller must
// not be able to enumerate the directory through the address-book endpoint.
func TestResolveRequiresSession(t *testing.T) {
	ts := newGALServer(t)
	c := &http.Client{} // no cookie jar: no session
	if code, _ := get(t, c, ts.URL+"/resolve?q=al"); code != http.StatusUnauthorized {
		t.Fatalf("GET /resolve without a session = %d, want 401", code)
	}
}

// TestResolveSuggest checks the autocomplete path: a typed query returns the GAL
// addresses that contain it, and excludes the ones that do not.
func TestResolveSuggest(t *testing.T) {
	ts := newGALServer(t)
	c := authedClient(t, ts)

	code, body := get(t, c, ts.URL+"/resolve?q=al")
	if code != http.StatusOK {
		t.Fatalf("GET /resolve?q=al = %d, want 200", code)
	}
	var got struct {
		Suggestions []galSuggestion `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v (body %q)", err, body)
	}
	// "al" is a substring of alice and albert, but not bob.
	addrs := map[string]bool{}
	for _, s := range got.Suggestions {
		addrs[s.Address] = true
		if s.Display != s.Address {
			t.Errorf("display %q should mirror address %q", s.Display, s.Address)
		}
	}
	if !addrs["alice@hermex.test"] || !addrs["albert@hermex.test"] {
		t.Errorf("suggestions %v missing alice/albert", got.Suggestions)
	}
	if addrs["bob@hermex.test"] {
		t.Errorf("bob should not match query \"al\": %v", got.Suggestions)
	}
}

// TestResolveCheckNames exercises name resolution across all three outcomes plus
// a direct address match: a full known address resolves, a partial that matches
// one user resolves to it, a partial that matches several is ambiguous, and an
// unknown name is unresolved. Order is preserved.
func TestResolveCheckNames(t *testing.T) {
	ts := newGALServer(t)
	c := authedClient(t, ts)

	field := "bob@hermex.test, albe, al, ghost"
	code, body := get(t, c, ts.URL+"/resolve?check="+url.QueryEscape(field))
	if code != http.StatusOK {
		t.Fatalf("GET /resolve?check = %d, want 200", code)
	}
	var got struct {
		Results []resolveResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v (body %q)", err, body)
	}
	if len(got.Results) != 4 {
		t.Fatalf("got %d results, want 4: %v", len(got.Results), got.Results)
	}

	// bob@hermex.test resolves directly (a known mailbox).
	if r := got.Results[0]; r.Status != "resolved" || len(r.Matches) != 1 || r.Matches[0].Address != "bob@hermex.test" {
		t.Errorf("result[0] (bob) = %+v, want resolved to bob", r)
	}
	// "albe" matches only albert: resolved to the single candidate.
	if r := got.Results[1]; r.Status != "resolved" || len(r.Matches) != 1 || r.Matches[0].Address != "albert@hermex.test" {
		t.Errorf("result[1] (albe) = %+v, want resolved to albert", r)
	}
	// "al" matches alice and albert: ambiguous, both offered.
	if r := got.Results[2]; r.Status != "ambiguous" || len(r.Matches) != 2 {
		t.Errorf("result[2] (al) = %+v, want ambiguous with 2 matches", r)
	}
	// "ghost" matches nobody: unresolved.
	if r := got.Results[3]; r.Status != "unresolved" || len(r.Matches) != 0 {
		t.Errorf("result[3] (ghost) = %+v, want unresolved", r)
	}
}
