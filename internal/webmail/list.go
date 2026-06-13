package webmail

import (
	"sort"
	"strconv"

	"hermex/internal/objectstore"
)

// pageSize is the number of messages shown per page of the message list.
const pageSize = 50

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
func listFolderPage(st *objectstore.Store, folderID int64, folder string, p listParams) (pageResult, error) {
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
		res.Messages = append(res.Messages, messageViewFrom(folderID, folder, m))
	}
	return res, nil
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
