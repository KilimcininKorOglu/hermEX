package webmail2api

import (
	"encoding/json"
	"net/http"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// folderRightsOf reads the rights user holds on each delegate folder.
func folderRightsOf(t *testing.T, dir, user string) map[int64]uint32 {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	out := map[int64]uint32{}
	for _, fid := range objectstore.DelegateFolders {
		r, err := st.ResolvePermission(fid, user)
		if err != nil {
			t.Fatal(err)
		}
		out[fid] = r
	}
	return out
}

// listedRights reads the access string the delegation list shows for grantee.
func listedRights(t *testing.T, do requestFunc, grantee string) (string, bool) {
	t.Helper()
	rec := do(http.MethodGet, "/api/v1/delegations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET delegations = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Delegations []struct {
			Grantee string `json:"grantee"`
			Rights  string `json:"rights"`
		} `json:"delegations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, d := range out.Delegations {
		if d.Grantee == grantee {
			return d.Rights, true
		}
	}
	return "", false
}

// TestDelegationAccessGrantsTheDelegateFolders proves the access picked in webmail
// is the permission every other surface enforces: read makes the delegate a
// reviewer of the six delegate folders, read and write an editor, and the list
// shows what the folders hold.
func TestDelegationAccessGrantsTheDelegateFolders(t *testing.T) {
	for _, tc := range []struct {
		body      string
		wantBits  uint32
		wantNoBit uint32
		listed    string
	}{
		{`{"grantee":"bob@hermex.test","rights":["read"]}`, mapi.FrightsReadAny, mapi.FrightsDeleteAny, "read"},
		{`{"grantee":"bob@hermex.test","rights":["read","write"]}`, mapi.FrightsReadAny | mapi.FrightsDeleteAny | mapi.FrightsCreate, 0, "read,write"},
	} {
		do, dir := delegationHarness(t)
		if rec := do(http.MethodPost, "/api/v1/delegations", tc.body); rec.Code != http.StatusOK {
			t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
		}
		for fid, r := range folderRightsOf(t, dir, "bob@hermex.test") {
			if r&tc.wantBits != tc.wantBits || r&tc.wantNoBit != 0 {
				t.Errorf("%s: folder %#x rights %#x, want %#x set and %#x clear", tc.body, fid, r, tc.wantBits, tc.wantNoBit)
			}
		}
		if got, _ := listedRights(t, do, "bob@hermex.test"); got != tc.listed {
			t.Errorf("%s: listed rights %q, want %q", tc.body, got, tc.listed)
		}
	}
}

// TestDelegationListShowsAccessGrantedElsewhere proves a delegate whose folder
// access came from Outlook or EWS shows that access, not "no access".
func TestDelegationListShowsAccessGrantedElsewhere(t *testing.T) {
	do, dir := delegationHarness(t)
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = st.ModifyPermissions(mapi.PrivateFIDCalendar, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: "carol@hermex.test", Rights: mapi.NormalizeRights(mapi.RightsEditor, true)},
	})
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := listedRights(t, do, "carol@hermex.test"); got != "read,write" {
		t.Errorf("carol listed rights %q, want read,write", got)
	}
	if got, _ := listedRights(t, do, "dave@hermex.test"); got != "" {
		t.Errorf("dave listed rights %q, want none", got)
	}
}

// TestRemovingADelegationRevokesFolderAccess proves a delete takes the delegate's
// folder permissions away with the list entries.
func TestRemovingADelegationRevokesFolderAccess(t *testing.T) {
	do, dir := delegationHarness(t)
	if rec := do(http.MethodPost, "/api/v1/delegations", `{"grantee":"bob@hermex.test","rights":["read","write"]}`); rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	bob := listDelegations(t, do)["bob@hermex.test"]
	if rec := do(http.MethodDelete, "/api/v1/delegations/"+bob.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	for fid, r := range folderRightsOf(t, dir, "bob@hermex.test") {
		if r&(mapi.FrightsReadAny|mapi.FrightsDeleteAny|mapi.FrightsCreate) != 0 {
			t.Errorf("folder %#x keeps rights %#x after the delete", fid, r)
		}
	}
}
