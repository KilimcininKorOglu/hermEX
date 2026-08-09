package mapihttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReadBodyRespectsTheOperatorLimit proves the request-body cap is read live from
// the operator-set value, so an edit (applied by the MAPI/HTTP daemon's poll) decides
// what is accepted with no restart: a body over the cap is truncated, and restoring
// the default admits the same body whole.
func TestReadBodyRespectsTheOperatorLimit(t *testing.T) {
	body := strings.Repeat("A", 1024)
	read := func() []byte {
		return readBody(httptest.NewRequest(http.MethodPost, "/mapi/emsmdb", strings.NewReader(body)))
	}

	SetMaxRequestBody(16)
	defer SetMaxRequestBody(0)
	if got := read(); len(got) != 16 {
		t.Errorf("body under a 16-byte cap read %d bytes, want 16", len(got))
	}

	// Restoring the default (0) admits the whole body again.
	SetMaxRequestBody(0)
	if got := read(); len(got) != len(body) {
		t.Errorf("body under the default cap read %d bytes, want %d", len(got), len(body))
	}
}

// TestSetMaxRequestBodyRejectsNegative proves a nonsense value falls back to the
// built-in default rather than capping every request at zero bytes, which would take
// the transport down instead of protecting it.
func TestSetMaxRequestBodyRejectsNegative(t *testing.T) {
	defer SetMaxRequestBody(0)
	SetMaxRequestBody(-1)
	got := readBody(httptest.NewRequest(http.MethodPost, "/mapi/emsmdb", strings.NewReader("hello")))
	if string(got) != "hello" {
		t.Errorf("body under a negative cap = %q, want the whole request", got)
	}
}
