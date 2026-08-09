package objectstore

import (
	"fmt"
	"strings"
)

// ListOptions selects, orders and bounds one page of a folder's listing. It
// exists so a paged reader costs what the page costs: ListMessages reads the
// whole folder, which is what IMAP needs but turns every inbox page turn on a
// long-lived mailbox into a full scan and sort.
type ListOptions struct {
	// Unread and Flagged narrow the listing; at most one is meaningful, and
	// Unread wins when both are set.
	Unread  bool
	Flagged bool
	// Sort names the ordering column: "sender", "subject", "size", or "" for the
	// arrival time. An unknown value falls back to arrival time, so a client
	// cannot reach anything that is not on this list.
	Sort string
	Desc bool
	// Limit bounds the page (0 = the whole selection); Offset skips into it.
	Limit  int
	Offset int
}

// MessagePage is one page of a folder listing with the counts a pager needs:
// Total is how many messages match the filter, Unread how many of the folder's
// messages are unread whatever the filter, since that is the badge.
type MessagePage struct {
	Messages []MessageInfo
	Total    int
	Unread   int
}

// orderBy maps a sort name to an index expression. The mapping is a fixed set,
// never the caller's string, so the ordering cannot be steered into the query.
// Text keys sort case-insensitively to match how a listing reads, and arrival
// time is the tiebreak so equal keys never reorder between requests.
func (o ListOptions) orderBy() string {
	dir := "ASC"
	if o.Desc {
		dir = "DESC"
	}
	var key string
	switch o.Sort {
	case "sender":
		key = "lower(sender)"
	case "subject":
		key = "lower(subject)"
	case "size":
		key = "size"
	default:
		return fmt.Sprintf("received %s, uid %s", dir, dir)
	}
	return fmt.Sprintf("%s %s, received %s, uid %s", key, dir, dir, dir)
}

// where builds the filter clause and its arguments.
func (o ListOptions) where(folderID int64) (string, []any) {
	clause := "folder_id=?"
	args := []any{folderID}
	switch {
	case o.Unread:
		clause += " AND read=0"
	case o.Flagged:
		clause += " AND flagged=1"
	}
	return clause, args
}

// ListMessagesPage returns one page of a folder's messages with the filter,
// ordering and bounds applied by the query rather than by the caller, so the work
// is proportional to the page and not to the folder.
func (s *Store) ListMessagesPage(folderID int64, opt ListOptions) (MessagePage, error) {
	clause, args := opt.where(folderID)

	var page MessagePage
	if err := s.idxdb.QueryRow(`SELECT COUNT(*) FROM messages WHERE `+clause, args...).Scan(&page.Total); err != nil {
		return MessagePage{}, err
	}
	// The unread count is the folder's, not the page's or the filter's: it is the
	// badge, and a listing filtered to starred mail must not change it.
	if err := s.idxdb.QueryRow(
		`SELECT COALESCE(SUM(CASE WHEN read=0 THEN 1 ELSE 0 END), 0) FROM messages WHERE folder_id=?`,
		folderID).Scan(&page.Unread); err != nil {
		return MessagePage{}, err
	}

	query := `SELECT ` + messageInfoCols + ` FROM messages WHERE ` + clause + ` ORDER BY ` + opt.orderBy()
	if opt.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, opt.Limit, max(opt.Offset, 0))
	}
	rows, err := s.idxdb.Query(query, args...)
	if err != nil {
		return MessagePage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanMessageInfo(rows)
		if err != nil {
			return MessagePage{}, err
		}
		page.Messages = append(page.Messages, m)
	}
	return page, rows.Err()
}

// SortKey reports whether name is an ordering this store can apply, normalizing
// it to the value ListOptions takes. It lets a caller map its own query
// parameters without repeating the set.
func SortKey(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "from", "sender":
		return "sender"
	case "subject":
		return "subject"
	case "size":
		return "size"
	default:
		return ""
	}
}
