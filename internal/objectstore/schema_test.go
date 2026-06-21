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

func TestOpenCreatesSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "alice")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	wantObjects := []string{
		"allocated_eids", "attachment_properties", "attachments", "autoreply_ts",
		"configurations", "folder_properties", "folders", "message_changes",
		"message_properties", "messages", "msgtime_index", "named_properties",
		"permissions", "receive_table", "recipients", "recipients_properties",
		"replguidmap", "rules", "search_result", "search_scopes", "store_properties",
	}
	slices.Sort(wantObjects)
	if got := tableNames(t, s.objdb); !slices.Equal(got, wantObjects) {
		t.Errorf("object store tables:\n got  %v\n want %v", got, wantObjects)
	}

	wantIndex := []string{"folders", "mapping", "messages"}
	if got := tableNames(t, s.idxdb); !slices.Equal(got, wantIndex) {
		t.Errorf("index tables:\n got  %v\n want %v", got, wantIndex)
	}

	// The schema version is recorded on the object store root.
	var v int
	if err := s.objdb.QueryRow(`SELECT config_value FROM configurations WHERE config_id=?`, cfgSchemaVersion).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != objectSchemaVersion {
		t.Errorf("object schema version = %d, want %d", v, objectSchemaVersion)
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
	setObjectVersion(t, dir, objectSchemaVersion+1)
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatalf("opened a store at version %d (binary supports %d); want refusal", objectSchemaVersion+1, objectSchemaVersion)
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
	if got := tableNames(t, s2.objdb); len(got) != 21 {
		t.Errorf("object store has %d tables after reopen, want 21", len(got))
	}
}
