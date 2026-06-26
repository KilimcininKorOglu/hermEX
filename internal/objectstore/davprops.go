package objectstore

// WebDAV dead properties (RFC 4918 §9.2) stored per collection folder. A property
// is keyed by its "{namespace} local" name and stored as the verbatim XML element a
// PROPPATCH set, so PROPFIND can replay it unchanged. These are opaque to the store:
// it neither parses nor interprets the value.

// SetDeadProp stores (or replaces) a collection's dead property.
func (s *Store) SetDeadProp(folderID int64, name, raw string) error {
	_, err := s.objdb.Exec(
		`INSERT INTO dav_dead_props (folder_id, name, raw) VALUES (?, ?, ?)
		 ON CONFLICT(folder_id, name) DO UPDATE SET raw=excluded.raw`,
		folderID, name, raw)
	return err
}

// RemoveDeadProp deletes a collection's dead property. Removing one that is absent
// is a no-op (matching PROPPATCH remove semantics).
func (s *Store) RemoveDeadProp(folderID int64, name string) error {
	_, err := s.objdb.Exec(
		`DELETE FROM dav_dead_props WHERE folder_id=? AND name=?`, folderID, name)
	return err
}

// DeadProp is one stored WebDAV dead property: its "{namespace} local" name and the
// verbatim XML element to replay.
type DeadProp struct {
	Name string
	Raw  string
}

// ListDeadProps returns a collection's dead properties ordered by name for a stable
// PROPFIND rendering.
func (s *Store) ListDeadProps(folderID int64) ([]DeadProp, error) {
	rows, err := s.objdb.Query(
		`SELECT name, raw FROM dav_dead_props WHERE folder_id=? ORDER BY name`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeadProp
	for rows.Next() {
		var p DeadProp
		if err := rows.Scan(&p.Name, &p.Raw); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
