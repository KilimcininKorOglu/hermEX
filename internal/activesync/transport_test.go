package activesync

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/wbxml"
)

// TestMaxBodyLimitResolution proves the request-body cap resolves to the operator-set
// value when set and the built-in default otherwise — the value the daemon's poll
// drives, read live at each request.
func TestMaxBodyLimitResolution(t *testing.T) {
	defer SetMaxRequestBody(0)
	if got := maxBodyLimit(); got != defaultMaxRequestBody {
		t.Errorf("default = %d, want %d", got, defaultMaxRequestBody)
	}
	SetMaxRequestBody(123456)
	if got := maxBodyLimit(); got != 123456 {
		t.Errorf("after set = %d, want 123456", got)
	}
	SetMaxRequestBody(0) // 0 restores the default
	if got := maxBodyLimit(); got != defaultMaxRequestBody {
		t.Errorf("after reset = %d, want the default %d", got, defaultMaxRequestBody)
	}
}

// TestReadWBXMLRespectsBodyLimit proves the cap is enforced end-to-end: a body over the
// limit is truncated and fails to decode, and restoring the default admits the same
// body, with no restart.
func TestReadWBXMLRespectsBodyLimit(t *testing.T) {
	body := wbxml.Marshal(wbxml.Str(wbxml.ASSyncKey, strings.Repeat("x", 200)))

	SetMaxRequestBody(4) // far smaller than the encoded body
	defer SetMaxRequestBody(0)
	r := httptest.NewRequest("POST", "/Microsoft-Server-ActiveSync", bytes.NewReader(body))
	if _, err := readWBXML(r); err == nil {
		t.Error("WBXML over the 4-byte cap decoded, want a truncation error")
	}

	SetMaxRequestBody(0)
	r2 := httptest.NewRequest("POST", "/Microsoft-Server-ActiveSync", bytes.NewReader(body))
	if _, err := readWBXML(r2); err != nil {
		t.Errorf("WBXML under the default cap = %v, want success", err)
	}
}
