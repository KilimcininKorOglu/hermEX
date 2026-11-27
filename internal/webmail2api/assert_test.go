package webmail2api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// The API tests are assertion-dense: one test drives a handler and then checks a
// dozen JSON fields, each written as its own if. That is what pushes these
// functions past the complexity budget and buries the one line that states what
// the test is about. The helpers here carry the comparison, the status check and
// the decode so a test body reads as the list of facts it asserts.

// apiHarness provisions a fresh mailbox and returns a request function that
// carries a valid session for it, the shape nearly every endpoint test needs.
func apiHarness(t *testing.T) (requestFunc, string) {
	t.Helper()
	dir := freshMailbox(t)
	return apiHarnessFor(t, dir), dir
}

// apiDo is apiHarness for a test that only makes requests.
func apiDo(t *testing.T) requestFunc {
	t.Helper()
	return apiHarnessFor(t, freshMailbox(t))
}

// freshMailbox provisions an empty mailbox, as account creation does.
func freshMailbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open mailbox", err)
	mustClose(t, st)
	return dir
}

// mustEncode encodes v and fails the test when it cannot, so a request body is
// never sent as an empty encoding.
func mustEncode(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode %T: %v", v, err)
	}
	return b
}

// mustClose closes c and fails the test when the close reports an error, so a
// store or body that cannot be released is a failure rather than a silent leak.
func mustClose(t testing.TB, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// apiHarnessFor returns a request function against an existing mailbox
// directory, for a test that seeds the store itself before serving.
func apiHarnessFor(t *testing.T, dir string) requestFunc {
	t.Helper()
	secret := []byte("webmail2api-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	return func(method, target, body string) *httptest.ResponseRecorder {
		token := mustMintToken(t, secret, sessionClaims{
			Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix(),
		})
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
}

// wantEq fails when got differs from want, naming the field in the message.
func wantEq[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// wantContains fails when got does not carry the substring the test requires.
func wantContains(t *testing.T, label, got, substr string) {
	t.Helper()
	if !strings.Contains(got, substr) {
		t.Errorf("%s = %q, want it to contain %q", label, got, substr)
	}
}

// mustMintToken signs a session token and fails the test when signing fails, so
// a request never goes out with an empty session cookie and passes as an
// unauthenticated one.
func mustMintToken(t *testing.T, secret []byte, c sessionClaims) string {
	t.Helper()
	token, err := mintToken(secret, c)
	mustNoErr(t, "mint the session token", err)
	return token
}

// mustProp reads tag from pv as a T and stops the test when the property is
// missing or holds another type.
func mustProp[T any](t *testing.T, what string, pv mapi.PropertyValues, tag mapi.PropTag) T {
	t.Helper()
	v, ok := pv.Get(tag)
	if !ok {
		t.Fatalf("%s: property %#x is not set", what, uint32(tag))
	}
	got, ok := v.(T)
	if !ok {
		t.Fatalf("%s: property %#x holds %T", what, uint32(tag), v)
	}
	return got
}

// mustNoErr fails the test when err is set, naming the operation.
func mustNoErr(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantStatus fails the test when a response carries the wrong status, printing
// the body so the reason is visible.
func wantStatus(t *testing.T, what string, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: status %d, want %d (body %s)", what, rec.Code, want, rec.Body.String())
	}
}

// decodeBody decodes a JSON response body into T, failing the test when it is
// not the shape the endpoint promises.
func decodeBody[T any](t *testing.T, what string, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v (body %s)", what, err, rec.Body.String())
	}
	return v
}

// okBody asserts a 200 and decodes the body, the shape almost every read
// assertion in these tests takes.
func okBody[T any](t *testing.T, what string, rec *httptest.ResponseRecorder) T {
	t.Helper()
	wantStatus(t, what, rec, http.StatusOK)
	return decodeBody[T](t, what, rec)
}
