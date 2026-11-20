package activesync

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/wbxml"
)

// TestMaxBodyLimitResolution proves the request-body cap resolves to the operator-set
// value when set and the built-in default otherwise, the value the daemon's poll
// drives, read live at each request.
func TestMaxBodyLimitResolution(t *testing.T) {
	defer SetMaxRequestBody(0)
	if got := maxBodyLimit(); got != DefaultMaxRequestBody {
		t.Errorf("default = %d, want %d", got, DefaultMaxRequestBody)
	}
	SetMaxRequestBody(123456)
	if got := maxBodyLimit(); got != 123456 {
		t.Errorf("after set = %d, want 123456", got)
	}
	SetMaxRequestBody(0) // 0 restores the default
	if got := maxBodyLimit(); got != DefaultMaxRequestBody {
		t.Errorf("after reset = %d, want the default %d", got, DefaultMaxRequestBody)
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

	// A valid document followed by more bytes, with the cap at the document's own
	// length: cut at the cap the prefix decodes, so only refusing the oversized body
	// whole keeps the server from running a request it never fully read.
	extra := append(append([]byte{}, body...), make([]byte, 64)...)
	SetMaxRequestBody(int64(len(body)))
	r3 := httptest.NewRequest("POST", "/Microsoft-Server-ActiveSync", bytes.NewReader(extra))
	if _, err := readWBXML(r3); err == nil {
		t.Error("a body over the cap decoded from its prefix, want a refusal")
	}
	r4 := httptest.NewRequest("POST", "/Microsoft-Server-ActiveSync", bytes.NewReader(body))
	if _, err := readWBXML(r4); err != nil {
		t.Errorf("a body exactly at the cap = %v, want success", err)
	}

	SetMaxRequestBody(0)
	r2 := httptest.NewRequest("POST", "/Microsoft-Server-ActiveSync", bytes.NewReader(body))
	if _, err := readWBXML(r2); err != nil {
		t.Errorf("WBXML under the default cap = %v, want success", err)
	}
}
