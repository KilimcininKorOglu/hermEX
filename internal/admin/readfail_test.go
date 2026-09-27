package admin

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// errReadFailed is the failure a read method returns when a test names it in
// fakeDir.readErrs.
var errReadFailed = errors.New("directory unreachable")

// TestAFailedReadHidesTheFormItFeeds proves a page does not offer a form built
// from a read that failed. That form showed empty or default values, and saving it
// replaced what is stored, so the page reports the failure in the form's place.
func TestAFailedReadHidesTheFormItFeeds(t *testing.T) {
	for _, tc := range []struct {
		read, path, form, what string
	}{
		{"GetDefaultSyncPolicy", "/admin/ui/syncpolicy", `hx-put="/admin/ui/syncpolicy"`, "the default device policy"},
	} {
		d := &fakeDir{
			authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
			readErrs: map[string]error{tc.read: errReadFailed},
		}
		ts := adminServer(t, d)
		session, _ := loginCookies(t, ts)

		page := wantBody(t, authedGET(t, ts, tc.path, session), http.StatusOK, tc.path)
		wantContains(t, page, "Could not read "+tc.what, tc.read+": the failed read is reported")
		if strings.Contains(page, tc.form) {
			t.Errorf("%s: %s offers the form after the read failed", tc.read, tc.path)
		}
	}
}
