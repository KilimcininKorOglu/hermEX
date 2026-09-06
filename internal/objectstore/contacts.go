package objectstore

import (
	"net/mail"
	"strings"

	"hermex/internal/mapi"
)

// contactEmailNames are the three contact e-mail slots (PidLidEmail{1,2,3}Address,
// PSETID_Address) ContactHasAddress matches a sender against.
var contactEmailNames = []mapi.PropertyName{
	mapi.NameEmail1Address,
	mapi.NameEmail2Address,
	mapi.NameEmail3Address,
}

// ContactHasAddress reports whether the mailbox's Contacts folder holds a contact
// carrying the given e-mail address in any of its three e-mail slots. It backs the
// out-of-office "known senders only" external audience: an external auto-reply is
// withheld unless the sender is a known contact.
//
// The match is case-insensitive on the bare addr-spec (display name and angle
// brackets dropped); a blank address never matches. Named-property ids are
// resolved without allocation (create=false), so a mailbox that has never stored a
// contact e-mail resolves no ids and reports false without scanning the folder.
func (s *Store) ContactHasAddress(address string) (bool, error) {
	want := normalizeContactAddress(address)
	if want == "" {
		return false, nil
	}
	tags, err := s.contactEmailTags()
	if err != nil {
		return false, err
	}
	if len(tags) == 0 {
		return false, nil // no contact e-mail named ids allocated, so nothing to match
	}
	objs, err := s.ListFolderObjects(int64(mapi.PrivateFIDContacts))
	if err != nil {
		return false, err
	}
	for _, obj := range objs {
		pv, err := s.GetMessageProperties(obj.ID, tags...)
		if err != nil {
			continue
		}
		if bagHasAddress(pv, tags, want) {
			return true, nil
		}
	}
	return false, nil
}

// contactEmailTags resolves this store's tags for a contact's three e-mail
// slots. It never allocates, so a mailbox that has never stored a contact
// resolves to nothing.
func (s *Store) contactEmailTags() ([]mapi.PropTag, error) {
	ids, err := s.GetNamedPropIDs(false, contactEmailNames)
	if err != nil {
		return nil, err
	}
	var tags []mapi.PropTag
	for _, id := range ids {
		if id != 0 {
			tags = append(tags, mapi.MakeTag(id, mapi.PtUnicode))
		}
	}
	return tags, nil
}

// bagHasAddress reports whether any of the tags carries the wanted address,
// compared in the normalized form both sides were reduced to.
func bagHasAddress(pv mapi.PropertyValues, tags []mapi.PropTag, want string) bool {
	for _, tag := range tags {
		v, ok := pv.Get(tag)
		if !ok {
			continue
		}
		if str, ok := v.(string); ok && normalizeContactAddress(str) == want {
			return true
		}
	}
	return false
}

// ContactMatch is one contact an autocomplete query matched: the name to show
// and the address to send to.
type ContactMatch struct {
	DisplayName string
	Address     string
}

// SearchContacts returns the mailbox's contacts whose display name or any of
// their three e-mail slots contains the query, newest first, up to limit.
//
// The filter runs in the query rather than over a listing, because this is
// called on every keystroke of a recipient field: reading each contact's
// properties back one object at a time would cost the size of the address book
// per character typed, instead of the size of the answer.
//
// A query of two characters or fewer matches nothing. A single letter matches
// most of an address book, and the client is still typing.
func (s *Store) SearchContacts(query string, limit int) ([]ContactMatch, error) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 3 || limit <= 0 {
		return nil, nil
	}
	tags, err := s.contactSearchTags()
	if err != nil || len(tags) == 0 {
		return nil, err
	}
	ids, err := s.contactIDsMatching(tags, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ContactMatch, 0, len(ids))
	for _, id := range ids {
		if m, ok := s.contactMatch(id, tags); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// contactSearchTags returns the property tags an autocomplete query is matched
// against: the display name and the three e-mail slots. Named ids are resolved
// without allocating, so a mailbox that has never stored a contact e-mail
// resolves none and the search answers nothing without touching the folder.
func (s *Store) contactSearchTags() ([]mapi.PropTag, error) {
	ids, err := s.GetNamedPropIDs(false, contactEmailNames)
	if err != nil {
		return nil, err
	}
	tags := []mapi.PropTag{mapi.PrDisplayName}
	for _, id := range ids {
		if id != 0 {
			tags = append(tags, mapi.PropTag(uint32(id)<<16|uint32(mapi.PtUnicode)))
		}
	}
	return tags, nil
}

// contactIDsMatching returns the ids of the contacts holding the query in one of
// the given properties.
func (s *Store) contactIDsMatching(tags []mapi.PropTag, query string, limit int) ([]int64, error) {
	args := []any{int64(mapi.PrivateFIDContacts)}
	placeholders := make([]string, len(tags))
	for i, t := range tags {
		placeholders[i] = "?"
		args = append(args, int64(uint32(t)))
	}
	args = append(args, "%"+escapeLike(query)+"%", limit)
	// LIKE with an explicit escape, so a query holding % or _ matches those
	// characters rather than acting as a pattern of its own.
	// #nosec G202 -- the only concatenated part is a generated list of ? placeholders, one per tag; every value is bound
	q := `SELECT DISTINCT m.message_id
	        FROM messages m
	        JOIN message_properties p ON p.message_id = m.message_id
	       WHERE m.parent_fid = ? AND m.is_deleted = 0 AND m.is_associated = 0
	         AND p.proptag IN (` + strings.Join(placeholders, ",") + `)
	         AND p.propval LIKE ? ESCAPE '\'
	       ORDER BY m.message_id DESC
	       LIMIT ?`
	rows, err := s.objdb.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// contactMatch reads one matched contact's name and first usable address. A
// contact with no address is dropped: it cannot be autocompleted into a
// recipient field.
func (s *Store) contactMatch(id int64, tags []mapi.PropTag) (ContactMatch, bool) {
	pv, err := s.GetMessageProperties(id, tags...)
	if err != nil {
		return ContactMatch{}, false
	}
	m := ContactMatch{}
	if v, ok := pv.Get(mapi.PrDisplayName); ok {
		m.DisplayName, _ = v.(string)
	}
	for _, tag := range tags {
		if tag == mapi.PrDisplayName {
			continue
		}
		if v, ok := pv.Get(tag); ok {
			if addr, _ := v.(string); strings.TrimSpace(addr) != "" {
				m.Address = strings.TrimSpace(addr)
				break
			}
		}
	}
	if m.Address == "" {
		return ContactMatch{}, false
	}
	return m, true
}

// escapeLike neutralizes the LIKE metacharacters so a query is matched as text.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(s)
}

// normalizeContactAddress reduces an e-mail address to a case-insensitive bare
// addr-spec for contact matching, dropping any display name and angle brackets.
// The empty string and the null return-path "<>" both reduce to "".
func normalizeContactAddress(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "<>" {
		return ""
	}
	if a, err := mail.ParseAddress(s); err == nil {
		return strings.ToLower(a.Address)
	}
	return strings.ToLower(strings.Trim(s, "<>"))
}
