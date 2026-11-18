package webmail2api

import (
	"encoding/json"
	"net/http"
	"testing"

	"hermex/internal/objectstore"
)

// TestLegacyFilterTargetsBecomeIDs proves a move or copy target stored by
// display name, from before folders were addressed by id, is read as the slug of
// the folder it reached; a target of another action type, a slug, and a name
// that reaches nothing are left alone; and the next read answers the same.
func TestLegacyFilterTargetsBecomeIDs(t *testing.T) {
	do, dir := folderHarness(t)
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the store", err)
	fid, err := st.CreateFolder(nil, "Project X")
	mustNoErr(t, "create Project X", err)
	legacy := []filterJSON{{ID: "1", Name: "f", Actions: []filterActionJSON{
		{Type: "moveToFolder", Target: "Project X"},
		{Type: "copyToFolder", Target: "trash"},
		{Type: "copyToFolder", Target: "Gone"},
		{Type: "categorize", Target: "Project X"},
	}}}
	raw, err := json.Marshal(legacy)
	mustNoErr(t, "marshal", err)
	mustNoErr(t, "write the legacy blob", saveSharedSettings(st, map[string]json.RawMessage{"webmail2Filters": raw}))
	st.Close()

	want := []string{customSlug(fid), "trash", "Gone", "Project X"}
	for _, pass := range []string{"first", "second"} {
		rec := do(http.MethodGet, "/api/v1/filters", "")
		var out struct {
			Filters []filterJSON `json:"filters"`
		}
		mustNoErr(t, pass+" decode", json.Unmarshal(rec.Body.Bytes(), &out))
		if len(out.Filters) != 1 {
			t.Fatalf("%s read: %d filters", pass, len(out.Filters))
		}
		for i, a := range out.Filters[0].Actions {
			wantEq(t, pass+" read: target of action "+a.Type, a.Target, want[i])
		}
	}
}
