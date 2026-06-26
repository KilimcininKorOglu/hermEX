package objectstore

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
)

// tableNames lists user tables (excluding sqlite internal ones) in a database.
func tableNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

// objectTargetVersion is the highest object-store schema version this binary
// supports: the baseline carried forward by every forward migration. A fresh
// store is migrated up to it on open, so it is the version a freshly created
// store records and the threshold above which a store is refused as too new.
func objectTargetVersion() int {
	target := objectSchemaVersion
	for _, m := range objectMigrations {
		if m.Version > target {
			target = m.Version
		}
	}
	return target
}

func TestOpenCreatesSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "alice")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	wantObjects := []string{
		"allocated_eids", "attachment_properties", "attachments", "autoreply_ts",
		"configurations", "dav_dead_props", "folder_properties", "folders",
		"message_changes", "message_properties", "messages", "msgtime_index",
		"named_properties", "permissions", "public_read_state", "receive_table",
		"recipients", "recipients_properties", "replguidmap", "rules",
		"search_result", "search_scopes", "store_properties",
	}
	slices.Sort(wantObjects)
	if got := tableNames(t, s.objdb); !slices.Equal(got, wantObjects) {
		t.Errorf("object store tables:\n got  %v\n want %v", got, wantObjects)
	}

	wantIndex := []string{"folders", "mapping", "messages", "vanished"}
	if got := tableNames(t, s.idxdb); !slices.Equal(got, wantIndex) {
		t.Errorf("index tables:\n got  %v\n want %v", got, wantIndex)
	}

	// The schema version is recorded on the object store root.
	var v int
	if err := s.objdb.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgSchemaVersion).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if want := objectTargetVersion(); v != want {
		t.Errorf("object schema version = %d, want %d", v, want)
	}
}

// setObjectVersion overwrites the recorded object-store schema version directly,
// to simulate a database written by a different binary.
func setObjectVersion(t *testing.T, dir string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(filepath.Join(dir, "objects.sqlite3")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE configurations SET config_value=? WHERE config_id=?`, v, cfgSchemaVersion); err != nil {
		t.Fatal(err)
	}
}

// TestOpenRefusesNewerSchema proves a store recorded newer than this binary is
// refused rather than opened — the downgrade guard that protects data written
// under a schema the binary does not understand.
func TestOpenRefusesNewerSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "future")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	target := objectTargetVersion()
	setObjectVersion(t, dir, target+1)
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatalf("opened a store at version %d (binary supports %d); want refusal", target+1, target)
	}
}

// TestOpenRefusesPreBaselineSchema proves a store below the baseline — a
// disposable pre-migration dev schema — is still refused.
func TestOpenRefusesPreBaselineSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ancient")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	setObjectVersion(t, dir, objectSchemaVersion-1)
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatalf("opened a pre-baseline store at version %d; want refusal", objectSchemaVersion-1)
	}
}

func TestReopenIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bob")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if got := tableNames(t, s2.objdb); len(got) != 23 {
		t.Errorf("object store has %d tables after reopen, want 23", len(got))
	}
}
