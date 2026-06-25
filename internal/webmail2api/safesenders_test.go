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
	st.Close()
	secret := []byte("safesenders-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	do := func(method, body string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
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
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
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
}
