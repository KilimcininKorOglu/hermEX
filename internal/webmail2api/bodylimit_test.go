package webmail2api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// jsonFiller is a syntactically valid compose body of exactly n bytes. The decoder
// has to read all of it before it can find fault with it, so a request that reads
// short was stopped by the cap rather than by its own contents.
func jsonFiller(n int) string {
	const head = `{"subject":"`
	const tail = `"}`
	return head + strings.Repeat("a", n-len(head)-len(tail)) + tail
}

// countingReader records how many bytes the server actually read from a request
// body, so a test can prove the cap stops the read rather than merely rejecting the
// request once the whole body is already in memory.
type countingReader struct {
	r    io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

// boundBodyServer builds an API server with a session cookie for a mailbox that is
// never opened: every case here is decided before a handler touches the store.
func boundBodyServer(t *testing.T) (*Server, string) {
	t.Helper()
	secret := []byte("body-limit-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email:   "alice@hermex.test",
		Mailbox: t.TempDir(),
		Exp:     time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, token
}

// sendBody posts a compose body of the given size and reports the status code and how
// many bytes of it the server read.
func sendBody(t *testing.T, srv *Server, token string, size int) (int, int64) {
	t.Helper()
	body := &countingReader{r: strings.NewReader(jsonFiller(size))}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/send", body)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, body.read
}

// TestOversizedRequestBodyIsRefused proves a compose body past the cap is refused and,
// more to the point, that the process stops reading it. Without the cap a single
// authenticated caller could stream an arbitrarily large body into the daemon that
// serves every mailbox on the instance.
func TestOversizedRequestBodyIsRefused(t *testing.T) {
	SetMaxRequestBody(1 << 20)
	t.Cleanup(func() { SetMaxRequestBody(0) })

	srv, token := boundBodyServer(t)
	code, read := sendBody(t, srv, token, 8<<20)
	if code == http.StatusOK {
		t.Fatalf("oversized send accepted: %d", code)
	}
	if read > 2<<20 {
		t.Errorf("read %d bytes of an 8 MiB body against a 1 MiB cap, want the read cut short", read)
	}
}

// TestRequestBodyUnderTheCapIsRead proves the cap does not disturb an ordinary
// request: the body is read whole and the handler answers on its own terms (here, a
// complaint about the missing recipient rather than about the body).
func TestRequestBodyUnderTheCapIsRead(t *testing.T) {
	SetMaxRequestBody(1 << 20)
	t.Cleanup(func() { SetMaxRequestBody(0) })

	srv, token := boundBodyServer(t)
	body := &countingReader{r: strings.NewReader(jsonFiller(64 << 10))}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/send", body)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if read := body.read; read != 64<<10 {
		t.Errorf("read %d bytes of a 64 KiB body under a 1 MiB cap, want all of it", read)
	}
	if got := rec.Body.String(); !strings.Contains(got, "recipient") {
		t.Errorf("send under the cap = %d %s, want the body decoded and the recipient check reached", rec.Code, got)
	}
}

// TestRequestBodyCapAppliesWithoutRestart proves the operator's saved value reaches
// live traffic: the daemon polls the stored limit and calls SetMaxRequestBody, and the
// very next request is judged against the new value. A cap read once at startup would
// need a restart, which is what the poll exists to avoid.
func TestRequestBodyCapAppliesWithoutRestart(t *testing.T) {
	t.Cleanup(func() { SetMaxRequestBody(0) })
	srv, token := boundBodyServer(t)

	// The built-in default (40 MiB) admits a 4 MiB body: it is read to the end and
	// answered on its contents, not its size.
	SetMaxRequestBody(0)
	if _, read := sendBody(t, srv, token, 4<<20); read != 4<<20 {
		t.Fatalf("read %d of a 4 MiB body under the default cap, want all of it", read)
	}

	// The operator lowers the cap; the next request is judged against it.
	SetMaxRequestBody(64 << 10)
	if code, read := sendBody(t, srv, token, 4<<20); code == http.StatusOK || read > 1<<20 {
		t.Errorf("after lowering the cap: code %d, read %d bytes, want a refusal with the read cut short", code, read)
	}
}

// TestRequestBodyCapIgnoresNonPositiveValues proves a stored zero (the row's "unset"
// state) restores the built-in default instead of capping every request at nothing,
// which would refuse all traffic.
func TestRequestBodyCapIgnoresNonPositiveValues(t *testing.T) {
	t.Cleanup(func() { SetMaxRequestBody(0) })

	SetMaxRequestBody(0)
	if got := maxRequestBody(); got != defaultMaxRequestBody {
		t.Errorf("cap after 0 = %d, want the built-in default %d", got, defaultMaxRequestBody)
	}
	SetMaxRequestBody(-1)
	if got := maxRequestBody(); got != defaultMaxRequestBody {
		t.Errorf("cap after a negative value = %d, want the built-in default %d", got, defaultMaxRequestBody)
	}
}
