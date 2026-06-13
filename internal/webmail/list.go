package webmail

import (
	"sort"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// pageSize is the number of messages shown per page of the message list.
const pageSize = 50

// orDefault returns v when it is non-empty, else def. It lets a URL parameter
// override a saved preference: pass the request value and the stored default.
func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// atoiDefault parses s as an int, returning def for empty or non-numeric input.
// The list pipeline clamps the page to the valid range, so a stray value here is
// harmless.
func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// listParams holds the message-list query parameters after whitelisting and
// clamping. Sort/Dir/Filter steer the pipeline; the handler fills them from the
// request (defaulting to a newest-first, unfiltered view).
type listParams struct {
	Sort   string // date | from | subject | size | flag | read
	Dir    string // desc | asc
	Filter string // all | unread
	Page   int    // 1-based; clamped to the available range by the pipeline
}

// pageResult is the computed message list: the visible page plus paging and
// folder-counter state. Total/Unread describe the whole folder (pre-filter), so
// the unread-only toggle never changes them.
type pageResult struct {
	Messages []messageView
	Total    int
	Unread   int
	Page     int // clamped current page
	MaxPage  int
	PrevPage int // 0 when there is no previous page
	NextPage int // 0 when there is no next page
}

// listFolderPage loads a folder's messages and applies the counter → filter →
// sort → paginate → map pipeline. Counters are taken before filtering (so they
// describe the folder, not the view); the pagination denominator is the
// post-filter count. Only the visible page is mapped to views. Paging is
// server-controlled but in memory: the folder is read in full, sorted, then
// sliced, because the sort key can be a field the index does not order by.
func listFolderPage(st *objectstore.Store, folderID int64, folder string, p listParams, cats []category) (pageResult, error) {
	msgs, err := st.ListMessages(folderID)
	if err != nil {
		return pageResult{}, err
	}

	res := pageResult{Total: len(msgs)}
	for _, m := range msgs {
		if m.Flags&objectstore.FlagSeen == 0 {
			res.Unread++
		}
	}

	if p.Filter == "unread" {
		kept := make([]objectstore.MessageInfo, 0, len(msgs))
		for _, m := range msgs {
			if m.Flags&objectstore.FlagSeen == 0 {
				kept = append(kept, m)
			}
		}
		msgs = kept
	}

	sortMessages(msgs, p.Sort, p.Dir)

	n := len(msgs)
	res.MaxPage = (n + pageSize - 1) / pageSize
	if res.MaxPage < 1 {
		res.MaxPage = 1
	}
	res.Page = p.Page
	if res.Page < 1 {
		res.Page = 1
	}
	if res.Page > res.MaxPage {
		res.Page = res.MaxPage
	}
	if res.Page > 1 {
		res.PrevPage = res.Page - 1
	}
	if res.Page < res.MaxPage {
		res.NextPage = res.Page + 1
	}

	lo := (res.Page - 1) * pageSize
	hi := lo + pageSize
	if lo > n {
		lo = n
	}
	if hi > n {
		hi = n
	}
	res.Messages = make([]messageView, 0, hi-lo)
	for _, m := range msgs[lo:hi] {
		v := messageViewFrom(folderID, folder, m)
		enrichIcons(st, m.ID, cats, &v)
		res.Messages = append(res.Messages, v)
	}
	return res, nil
}

// enrichIcons fills the icon-column flags that require reading the message
// object — the real-attachment paperclip and the importance markers. It is
// called only for the rows on the visible page, and lives outside
// messageViewFrom on purpose: the search path also maps views and must not pay
// for these per-message reads. A read error leaves the flag false (the icon is
// simply not shown).
func enrichIcons(st *objectstore.Store, messageID int64, cats []category, v *messageView) {
	if has, err := st.HasAttachments(messageID); err == nil {
		v.HasAttachment = has
	}
	if props, err := st.GetMessageProperties(messageID, mapi.PrImportance); err == nil {
		if val, ok := props.Get(mapi.PrImportance); ok {
			if n, ok := val.(int32); ok {
				v.ImportanceHigh = n == int32(mapi.ImportanceHigh)
				v.ImportanceLow = n == int32(mapi.ImportanceLow)
			}
		}
	}
	if f, err := st.GetFollowupFlag(messageID); err == nil {
		v.FlagComplete = f.Status == objectstore.FlagStatusComplete
		switch {
		case f.Color > 0:
			v.FlagColor = f.Color
		case !v.FlagComplete && v.Flagged:
			v.FlagColor = objectstore.FlagColorRed // legacy \Flagged with no follow-up color shows red
		}
	}
	if names, err := st.GetCategories(messageID); err == nil {
		for _, n := range names {
			v.Categories = append(v.Categories, categoryView{Name: n, Color: catColor(cats, n)})
		}
	}
}

// columnHeader is one sortable column heading in the message list. The handler
// precomputes the link state so the template stays free of URL/sort logic.
type columnHeader struct {
	Key     string // sort key carried in the link (matches sortMessages)
	Label   string // display text
	Active  bool   // the list is currently sorted by this column
	NextDir string // direction to request when the header is clicked
	Arrow   string // ▲ ascending / ▼ descending, shown only on the active column
}

// listColumns returns the message list's sortable column headers given the
// current sort key and direction. Clicking the active column toggles its
// direction; clicking another column starts at its natural direction (date
// newest-first, text A→Z).
func listColumns(sort, dir string) []columnHeader {
	defs := []struct{ key, label string }{
		{"from", "From"},
		{"subject", "Subject"},
		{"date", "Date"},
	}
	cols := make([]columnHeader, 0, len(defs))
	for _, d := range defs {
		c := columnHeader{Key: d.key, Label: d.label, Active: sort == d.key}
		switch {
		case c.Active && dir == "asc":
			c.NextDir, c.Arrow = "desc", "▲"
		case c.Active: // dir == "desc"
			c.NextDir, c.Arrow = "asc", "▼"
		case d.key == "date":
			c.NextDir = "desc" // first click on Date shows newest first
		default:
			c.NextDir = "asc" // first click on a text column sorts A→Z
		}
		cols = append(cols, c)
	}
	return cols
}

// sortMessages orders messages in place by the given key and direction. A strict
// final tiebreak on UID (unique within a folder) makes the order a total order,
// so the unstable sort is fully deterministic without needing a stable sort. An
// unknown key sorts by received date (the default newest-first view).
func sortMessages(msgs []objectstore.MessageInfo, key, dir string) {
	// less reports a<b in ascending terms for the chosen key, breaking ties by
	// ascending UID so the comparator is a strict total order.
	less := func(a, b objectstore.MessageInfo) bool {
		switch key {
		case "from":
			if a.Sender != b.Sender {
				return a.Sender < b.Sender
			}
		case "subject":
			if a.Subject != b.Subject {
				return a.Subject < b.Subject
			}
		case "size":
			if a.Size != b.Size {
				return a.Size < b.Size
			}
		case "flag":
			af, bf := a.Flags&objectstore.FlagFlagged != 0, b.Flags&objectstore.FlagFlagged != 0
			if af != bf {
				return bf // unflagged before flagged, ascending
			}
		case "read":
			ar, br := a.Flags&objectstore.FlagSeen != 0, b.Flags&objectstore.FlagSeen != 0
			if ar != br {
				return br // unread before read, ascending
			}
		default: // "date"
			if !a.InternalDate.Equal(b.InternalDate) {
				return a.InternalDate.Before(b.InternalDate)
			}
		}
		return a.UID < b.UID
	}
	sort.Slice(msgs, func(i, j int) bool {
		if dir == "desc" {
			return less(msgs[j], msgs[i])
		}
		return less(msgs[i], msgs[j])
	})
}
