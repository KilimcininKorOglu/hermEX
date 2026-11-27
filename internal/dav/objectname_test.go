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
	invalid := []string{
		"", ".", "..", "a/b", "../etc/passwd", `a\b`, "x\x00y", "/", `\`,
		"shell.php", "x.PHP", "page.html", "icon.svg", "run.exe", "a.js",
	}
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
	resp := doFullStatus(t, ts, "PUT", contactURL(`a\b.vcf`), adaVCard,
		map[string]string{"Content-Type": "text/vcard"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT unsafe name status %d, want 400", resp.StatusCode)
	}
}

// TestCopyMoveRejectUnsafeDestinationName names, in the Destination header, the
// same unsafe names a PUT refuses. The destination becomes the copy's stored
// resource name, so it must be refused with 400 as well and never filed.
func TestCopyMoveRejectUnsafeDestinationName(t *testing.T) {
	ts := davServer(t)
	doFull(t, ts, "PUT", contactURL("ada.vcf"), adaVCard, map[string]string{"Content-Type": "text/vcard"})
	for _, method := range []string{"COPY", "MOVE"} {
		for _, name := range []string{"page.html", "a%5Cb.vcf"} {
			resp := doFullStatus(t, ts, method, contactURL("ada.vcf"), "",
				map[string]string{"Destination": contactURL(name)})
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s to %q: status %d, want 400", method, name, resp.StatusCode)
			}
		}
	}
	if resp := doFullStatus(t, ts, "GET", contactURL("page.html"), "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unsafe destination name was filed: status %d, want 404", resp.StatusCode)
	}
}
