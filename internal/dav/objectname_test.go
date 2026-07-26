package dav

import (
	"net/http"
	"testing"
)

// TestValidObjectName pins the object-name guard: a stored DavResourceName is
// echoed back in PROPFIND/REPORT, so only a plain single path component is
// accepted and any separator or traversal token is rejected.
func TestValidObjectName(t *testing.T) {
	valid := []string{"ada.vcf", "event-1.ics", "a_b.2.vcf", "café.vcf", "UPPER.ICS"}
	for _, n := range valid {
		if !validObjectName(n) {
			t.Errorf("validObjectName(%q) = false, want true", n)
		}
	}
	invalid := []string{"", ".", "..", "a/b", "../etc/passwd", `a\b`, "x\x00y", "/", `\`}
	for _, n := range invalid {
		if validObjectName(n) {
			t.Errorf("validObjectName(%q) = true, want false", n)
		}
	}
}

// TestPutRejectsUnsafeName confirms a PUT whose object segment carries a
// path-separator (a backslash survives HTTP client path cleaning, unlike "..")
// is refused with 400 rather than stored as the reflected DavResourceName.
func TestPutRejectsUnsafeName(t *testing.T) {
	ts := davServer(t)
	resp, _ := doFull(t, ts, "PUT", contactURL(`a\b.vcf`), adaVCard,
		map[string]string{"Content-Type": "text/vcard"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT unsafe name status %d, want 400", resp.StatusCode)
	}
}

