package ews

// maxFindItems is the most items one FindItem returns, Exchange's
// EWSFindCountLimit default. A larger view is answered with its first page and
// IncludesLastItemInRange false, so the client pages on.
const maxFindItems = 1000

// indexedPageView is the IndexedPageItemView of a FindItem request
// ([MS-OXWSSRCH] 2.2.4.6): MaxEntriesReturned items starting Offset items from
// the BasePoint (Beginning or End).
type indexedPageView struct {
	MaxEntries int    `xml:"MaxEntriesReturned,attr"`
	Offset     int    `xml:"Offset,attr"`
	BasePoint  string `xml:"BasePoint,attr"`
}

// window returns the half-open range [from, to) of a total-item view that the
// page selects, clamped to the view and to maxFindItems. A nil view is the
// first maxFindItems items.
func (v *indexedPageView) window(total int) (from, to int) {
	size := maxFindItems
	if v == nil {
		return 0, min(total, size)
	}
	if v.MaxEntries > 0 {
		size = min(v.MaxEntries, maxFindItems)
	}
	offset := max(v.Offset, 0)
	if v.BasePoint == "End" {
		to = max(total-offset, 0)
		return max(to-size, 0), to
	}
	from = min(offset, total)
	return from, min(from+size, total)
}

// pageFindRoot cuts a FindItem answer to the requested page and sets the
// paging attributes. Each listing fills exactly one of the item slices, so the
// same window applies to whichever is set.
func pageFindRoot(root *findItemRoot, view *indexedPageView) {
	it := &root.Items
	total := len(it.Messages) + len(it.MeetingRequests) + len(it.Tasks) + len(it.CalendarItems) + len(it.BaseItems)
	from, to := view.window(total)
	it.Messages = cut(it.Messages, from, to)
	it.MeetingRequests = cut(it.MeetingRequests, from, to)
	it.Tasks = cut(it.Tasks, from, to)
	it.CalendarItems = cut(it.CalendarItems, from, to)
	it.BaseItems = cut(it.BaseItems, from, to)
	root.TotalItemsInView = total
	root.IncludesLastItemInRange = to == total
	if view != nil {
		if view.BasePoint == "End" {
			root.IncludesLastItemInRange = from == 0
		}
		// The next page starts past this one, counted from the same base point.
		next := min(max(view.Offset, 0), total) + to - from
		root.IndexedPagingOffset = &next
	}
}

// cut returns s[from:to], or s unchanged when it is empty.
func cut[T any](s []T, from, to int) []T {
	if len(s) == 0 {
		return s
	}
	return s[from:to]
}
