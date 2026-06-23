package webmail

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestFolderSharingLists checks that the sharing page lists who has access to a
// folder and names the matching permission level: a Reviewer grant shows the
// grantee and the "Reviewer" profile, not a raw bitmask.
func TestFolderSharingLists(t *testing.T) {
	path := emptyMailbox(t)
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ModifyPermissions(int64(mapi.PrivateFIDInbox), false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: "bob@hermex.test", Rights: mapi.RightsReviewer},
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ts := newTestServer(t, path)
	cl := authedClient(t, ts)
	code, body := get(t, cl, ts.URL+"/sharing?folder=INBOX")
	if code != 200 {
		t.Fatalf("sharing status %d, want 200", code)
	}
	if !strings.Contains(body, "bob@hermex.test") {
		t.Errorf("sharing page did not list the grantee:\n%s", body)
	}
	if !strings.Contains(body, "Reviewer") {
		t.Errorf("sharing page did not name the Reviewer level (raw bitmask leaked?):\n%s", body)
	}
}
