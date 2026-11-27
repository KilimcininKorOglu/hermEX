package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestIsSafeSender covers the matching rules that gate auto-loading remote images
// (a tracking-pixel decision, so it must be exact): an exact address, a bare
// domain, an "@domain" entry, case-insensitivity, and that subdomains do NOT widen.
func TestIsSafeSender(t *testing.T) {
	cases := []struct {
		list []string
		addr string
		want bool
	}{
		{[]string{"boss@hermex.test"}, "boss@hermex.test", true},
		{[]string{"BOSS@HERMEX.TEST"}, "boss@hermex.test", true},   // case-insensitive
		{[]string{"hermex.test"}, "anyone@hermex.test", true},      // bare domain
		{[]string{"@hermex.test"}, "anyone@hermex.test", true},     // @domain form
		{[]string{"hermex.test"}, "anyone@sub.hermex.test", false}, // no subdomain widening
		{[]string{"boss@hermex.test"}, "other@hermex.test", false},
		{[]string{"hermex.test"}, "", false},
		// A suffix is not the domain, there is no wildcard, and no prefix match.
		{[]string{"hermex.test"}, "x@evilhermex.test", false},
		{[]string{"*"}, "x@hermex.test", false},
		{[]string{"boss@hermex.test"}, "boss@hermex.test.evil", false},
		{nil, "boss@hermex.test", false},
	}
	for _, c := range cases {
		if got := isSafeSender(c.list, c.addr); got != c.want {
			t.Errorf("isSafeSender(%v, %q) = %v, want %v", c.list, c.addr, got, c.want)
		}
	}
}

// TestNormalizeSafeSenders proves the stored list is lowercased, trimmed,
// de-duplicated, and stripped of empties, so both webmail clients agree on it.
func TestNormalizeSafeSenders(t *testing.T) {
	got := normalizeSafeSenders([]string{" Boss@Hermex.Test ", "", "example.com", "boss@hermex.test", "EXAMPLE.COM"})
	want := []string{"boss@hermex.test", "example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeSafeSenders = %v, want %v", got, want)
	}
}

// TestSafeSendersRoundTrip proves the PUT/GET endpoints persist the allowlist in
// the shared settings blob and normalize it on write.
func TestSafeSendersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustClose(t, st)
	secret := []byte("safesenders-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, body string) *httptest.ResponseRecorder {
		token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, "/api/v1/safe-senders", nil)
		} else {
			req = httptest.NewRequest(method, "/api/v1/safe-senders", strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	list := func(rec *httptest.ResponseRecorder) []string {
		var out struct {
			SafeSenders []string `json:"safeSenders"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode safe senders: %v (status %d, body %s)", err, rec.Code, rec.Body.String())
		}
		return out.SafeSenders
	}

	if got := list(do(http.MethodGet, "")); len(got) != 0 {
		t.Fatalf("initial safe senders = %v, want empty", got)
	}
	if rec := do(http.MethodPut, `{"safeSenders":[" Boss@Hermex.Test ","","example.com","boss@hermex.test"]}`); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	got := list(do(http.MethodGet, ""))
	want := []string{"boss@hermex.test", "example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after put, safe senders = %v, want %v", got, want)
	}

	// A malformed or oversized write is refused and leaves the list as it was.
	huge := `{"safeSenders":["` + strings.Repeat("a", 2<<20) + `@x.test"]}`
	for _, body := range []string{`{`, `{"safeSenders":"boss@hermex.test"}`, huge} {
		if rec := do(http.MethodPut, body); rec.Code == http.StatusOK {
			t.Errorf("put of %.40q answered 200", body)
		}
	}
	if got := list(do(http.MethodGet, "")); !reflect.DeepEqual(got, want) {
		t.Errorf("after refused puts, safe senders = %v, want %v", got, want)
	}
}
