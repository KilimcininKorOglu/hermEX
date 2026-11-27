package directory

import (
	"errors"
	"strings"
	"testing"
)

// TestHealthTargetsRoundTrip proves an added target reads back in name order and
// a deleted one is gone, which is what lets the status page change without a
// restart.
func TestHealthTargetsRoundTrip(t *testing.T) {
	d := freshSQL(t)
	imapID, err := d.AddHealthTarget(HealthTarget{Name: " imap ", URL: " http://imap:8090/healthz "})
	mustNoErr(t, "add imap", err)
	davID, err := d.AddHealthTarget(HealthTarget{Name: "dav", URL: "https://dav:8090/healthz"})
	mustNoErr(t, "add dav", err)
	wantEq(t, "dav id > 0", davID > 0 && davID != imapID, true)

	got, err := d.ListHealthTargets()
	mustNoErr(t, "list", err)
	wantEq(t, "target count", len(got), 2)
	wantEq(t, "first by name", got[0].Name, "dav")
	wantEq(t, "trimmed name", got[1].Name, "imap")
	wantEq(t, "trimmed url", got[1].URL, "http://imap:8090/healthz")

	gone, err := d.DeleteHealthTarget(imapID)
	mustNoErr(t, "delete", err)
	wantEq(t, "a row went", gone, true)
	gone, err = d.DeleteHealthTarget(imapID)
	mustNoErr(t, "delete again", err)
	wantEq(t, "a second delete finds nothing", gone, false)
	got, err = d.ListHealthTargets()
	mustNoErr(t, "list after delete", err)
	wantEq(t, "targets left", len(got), 1)
	wantEq(t, "the target left", got[0].Name, "dav")
}

// TestHealthTargetValidation proves a target the monitor could not probe is
// refused before it is stored, and a taken name is reported as such.
func TestHealthTargetValidation(t *testing.T) {
	d := freshSQL(t)
	for _, bad := range []HealthTarget{
		{Name: "", URL: "http://imap:8090/healthz"},
		{Name: "imap", URL: ""},
		{Name: "imap", URL: "imap:8090/healthz"},
		{Name: "imap", URL: "ftp://imap/healthz"},
		{Name: "imap", URL: "http:///healthz"},
		{Name: "   ", URL: "http://imap:8090/healthz"},
		// One past the column widths (VARCHAR(64) name, VARCHAR(512) url).
		{Name: strings.Repeat("n", 65), URL: "http://imap:8090/healthz"},
		{Name: "imap", URL: "http://imap/" + strings.Repeat("p", 501)},
	} {
		badID, err := d.AddHealthTarget(bad)
		if !errors.Is(err, ErrInvalidHealthTarget) {
			t.Errorf("AddHealthTarget(%.80v) = %v, want ErrInvalidHealthTarget", bad, err)
		}
		wantEq(t, "refused invalid target id", badID, int64(0))
	}
	got, err := d.ListHealthTargets()
	mustNoErr(t, "list after the refusals", err)
	wantEq(t, "targets stored by refused adds", len(got), 0)

	imapID, err := d.AddHealthTarget(HealthTarget{Name: "imap", URL: "http://imap:8090/healthz"})
	mustNoErr(t, "add imap", err)
	wantEq(t, "added imap id > 0", imapID > 0, true)
	dupID, err := d.AddHealthTarget(HealthTarget{Name: "imap", URL: "http://other:8090/healthz"})
	if !errors.Is(err, ErrHealthTargetExists) {
		t.Errorf("a duplicate name = %v, want ErrHealthTargetExists", err)
	}
	wantEq(t, "refused duplicate target id", dupID, int64(0))
	got, err = d.ListHealthTargets()
	mustNoErr(t, "list after the duplicate", err)
	wantEq(t, "targets after the duplicate", len(got), 1)
	wantEq(t, "url kept through the duplicate", got[0].URL, "http://imap:8090/healthz")
}
