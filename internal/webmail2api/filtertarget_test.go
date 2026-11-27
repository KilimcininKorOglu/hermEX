package webmail2api

import (
	"encoding/json"
	"net/http"
	"testing"

	"hermex/internal/objectstore"
)

// TestAnUnconvertedFilterTargetStillDelivers proves a filter target stored by
// display name, which no read of the filters has converted yet, still files a
// delivery into the folder that name reached, while a slug resolves to its own
// folder and a name reaching nothing resolves nowhere.
func TestAnUnconvertedFilterTargetStillDelivers(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	mustNoErr(t, "open the store", err)
	defer mustClose(t, st)
	fid, err := st.CreateFolder(nil, "Project X")
	mustNoErr(t, "create Project X", err)
	for _, ref := range []string{"Project X", customSlug(fid)} {
		got, ok := resolveFilterFolder(st, ref)
		if !ok || got != fid {
			t.Errorf("target %q resolved to %d, %v; want %d", ref, got, ok, fid)
		}
	}
	for _, ref := range []string{"Gone", "f999999", "f13x", ""} {
		if got, ok := resolveFilterFolder(st, ref); ok {
			t.Errorf("target %q resolved to %d", ref, got)
		}
	}
}

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
	mustClose(t, st)

	want := []string{customSlug(fid), "trash", "Gone", "Project X"}
	for _, pass := range []string{"first", "second"} {
		rec := do(http.MethodGet, "/api/v1/filters", "")
		wantStatus(t, pass+" read", rec, http.StatusOK)
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
