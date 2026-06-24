package objectstore

// MarkPublicMessageRead records, in this (the caller's OWN) store, that the user
// has read the public-folder message identified by (owner, messageID). owner is
// the public store's owner key (its domain); messageID is the public message's
// stable, monotonic, never-reused EID. Marking is idempotent.
//
// Public read state is per-user by design: public-folder flags are shared
// org-wide, so a read must not write \Seen back to the shared store. Each user's
// reads live in their own store instead, where they persist and stay private to
// that user.
func (s *Store) MarkPublicMessageRead(owner string, messageID int64) error {
	_, err := s.objdb.Exec(
		`INSERT OR IGNORE INTO public_read_state (owner, message_id) VALUES (?, ?)`,
		owner, messageID)
	return err
}

// PublicReadSet returns the set of public message_ids the user has read for the
// given public store owner. Membership means read; absence means unread.
func (s *Store) PublicReadSet(owner string) (map[int64]bool, error) {
	rows, err := s.objdb.Query(
		`SELECT message_id FROM public_read_state WHERE owner=?`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = true
	}
	return set, rows.Err()
}
