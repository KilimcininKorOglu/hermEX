package nspi

import (
	"sort"
	"strings"

	"hermex/internal/mapi"
)

// midBase is the first entry MId. MIds 0x0–0xF are reserved ([MS-OXNSPI]:
// MID_BEGINNING_OF_TABLE=0x0, MID_CURRENT=0x1, MID_END_OF_TABLE=0x2), so entry
// MIds start at 0x10. galEnumLimit is the effective "enumerate all" cap.
const (
	midBase             uint32 = 0x10
	midBeginningOfTable uint32 = 0x0
	midEndOfTable       uint32 = 0x2
	galEnumLimit               = 100000
)

// galDNPrefix is the X500 DN stem for a GAL mailuser; the SMTP address is the
// final cn= component, so dnToSMTP reverses it without a live lookup.
const galDNPrefix = "/o=hermex/ou=hermex/cn=Recipients/cn="

// userDN builds a mailuser's reversible X500 DN.
func userDN(smtp string) string { return galDNPrefix + smtp }

// dnToSMTP recovers the SMTP address from a mailuser DN (the final cn=
// component), matching userDN. ok is false when dn is not a GAL mailuser DN.
func dnToSMTP(dn string) (string, bool) {
	i := strings.LastIndex(dn, "/cn=")
	if i < 0 {
		return "", false
	}
	smtp := dn[i+len("/cn="):]
	if smtp == "" {
		return "", false
	}
	return smtp, true
}

// NSPI name-resolution result codes ([MS-OXNSPI] 2.2.1.1), returned per input
// name by ResolveNamesW. They numerically alias the table bookmarks but form a
// distinct semantic space.
const (
	midUnresolved uint32 = 0x0
	midAmbiguous  uint32 = 0x1
	midResolved   uint32 = 0x2
)

// Address-book hide bits (the PR_ATTR_HIDDEN mask): each NSPI surface hides on
// its own bit, so an admin can hide a user from GAL browse yet keep them
// resolvable by name. GAL browse honors abHideFromGAL; name resolution honors
// abHideResolve. Direct fetches by a MId the client already holds are never
// hidden — asking for a specific entry opens it.
const (
	abHideFromGAL uint32 = 0x01
	abHideResolve uint32 = 0x08
)

// galUser is one GAL entry with its assigned MId. hidden is the PR_ATTR_HIDDEN
// mask the directory supplied; the surface applying it decides which bit matters.
type galUser struct {
	mid     uint32
	display string
	smtp    string
	hidden  uint32
}

// gal is the Global Address List in display-name order with MId assignment. It
// is recomputed per request (the STAT cursor is client-carried, so the server
// keeps no snapshot), and MId = midBase + index is stable as long as the GAL set
// is unchanged within a session.
type gal struct {
	users []galUser
}

// galLess is the total order the GAL is presented in for the display-name sort
// every NSPI client uses: case-insensitively by display name, with the unique
// SMTP address as the deterministic tiebreaker. The tiebreaker keeps the order
// stable across the per-request snapshot, so a client's cached MIds keep
// pointing at the same entry. SeekEntries searches with the same comparison.
func galLess(aDisplay, aSMTP, bDisplay, bSMTP string) bool {
	if ad, bd := strings.ToLower(aDisplay), strings.ToLower(bDisplay); ad != bd {
		return ad < bd
	}
	return strings.ToLower(aSMTP) < strings.ToLower(bSMTP)
}

// snapshot builds the GAL: every directory user, ordered for the display-name
// sort every NSPI client uses (galLess: display name, then address), each
// assigned a stable MId by position. An empty GAL (no GAL backing, or a lookup
// error) is a valid empty address book.
func (s *Server) snapshot() gal {
	if s.gal == nil {
		return gal{}
	}
	entries, err := s.gal.SearchGAL("", galEnumLimit)
	if err != nil {
		return gal{}
	}
	sort.Slice(entries, func(i, j int) bool {
		return galLess(entries[i].DisplayName, entries[i].Address, entries[j].DisplayName, entries[j].Address)
	})
	users := make([]galUser, len(entries))
	for i, e := range entries {
		users[i] = galUser{mid: midBase + uint32(i), display: e.DisplayName, smtp: e.Address, hidden: e.HiddenFrom}
	}
	return gal{users: users}
}

// byMID returns the user at an entry MId.
func (g gal) byMID(mid uint32) (galUser, bool) {
	i := int(mid) - int(midBase)
	if i < 0 || i >= len(g.users) {
		return galUser{}, false
	}
	return g.users[i], true
}

// position maps a STAT.cur_rec MId to a 0-based row index: the table-start and
// table-end bookmarks clamp to the ends, an entry MId maps to its position.
func (g gal) position(curRec uint32) int {
	switch curRec {
	case midBeginningOfTable:
		return 0
	case midEndOfTable:
		return len(g.users)
	}
	i := int(curRec) - int(midBase)
	if i < 0 {
		return 0
	}
	if i > len(g.users) {
		return len(g.users)
	}
	return i
}

// midAt returns the MId of the row at index i, or MID_END_OF_TABLE when i is at
// or past the end (the cursor has run off the table).
func (g gal) midAt(i int) uint32 {
	if i < 0 || i >= len(g.users) {
		return midEndOfTable
	}
	return g.users[i].mid
}

// galView is the hide-filtered cursor view of the GAL for table walks (QueryRows,
// SeekEntries, UpdateStat). vis holds the ascending full-snapshot indices of the
// users visible on the GAL-browse surface, so the cursor walks the subsequence
// while each row keeps its full-snapshot MId (direct GetProps by that MId still
// works). With nothing hidden, vis is every index and the view behaves exactly
// like the full GAL, so the existing cursor semantics and their tests are
// unchanged.
type galView struct {
	g   gal
	vis []int
}

// browseView builds the GAL-browse view: the users not hidden from the GAL
// (mask bit abHideFromGAL), preserving the display-name order of the snapshot.
func (g gal) browseView() galView {
	vis := make([]int, 0, len(g.users))
	for i, u := range g.users {
		if u.hidden&abHideFromGAL == 0 {
			vis = append(vis, i)
		}
	}
	return galView{g: g, vis: vis}
}

// total is the number of rows in the browse view.
func (v galView) total() int { return len(v.vis) }

// userAt returns the user at a visible-space row index (0 <= pos < total).
func (v galView) userAt(pos int) galUser { return v.g.users[v.vis[pos]] }

// position maps a STAT.cur_rec to a 0-based visible-space row index: the
// table-start/-end bookmarks clamp to the ends; an entry MId maps to the first
// visible row at or after its full-snapshot position, so a cursor parked on a
// now-hidden entry advances to the next visible one.
func (v galView) position(curRec uint32) int {
	switch curRec {
	case midBeginningOfTable:
		return 0
	case midEndOfTable:
		return len(v.vis)
	}
	full := int(curRec) - int(midBase)
	if full < 0 {
		return 0
	}
	return sort.Search(len(v.vis), func(i int) bool { return v.vis[i] >= full })
}

// midAt returns the MId of the visible row at index i, or MID_END_OF_TABLE when i
// is at or past the end of the view.
func (v galView) midAt(i int) uint32 {
	if i < 0 || i >= len(v.vis) {
		return midEndOfTable
	}
	return v.g.users[v.vis[i]].mid
}

// matchesToken reports whether a search token matches this user
// case-insensitively, as a substring of its SMTP address or display name. It is
// the single predicate shared by ResolveNamesW's resolve and GetMatches' PR_ANR
// restriction, so both return the same set for the same token.
func (u galUser) matchesToken(token string) bool {
	tok := strings.ToLower(token)
	return strings.Contains(strings.ToLower(u.smtp), tok) ||
		strings.Contains(strings.ToLower(u.display), tok)
}

// resolve matches a token (a name or address) against each user via
// matchesToken, mirroring the reference's resolve-node behavior: exactly one
// match resolves to that MId (midResolved), more than one is midAmbiguous, none
// is midUnresolved.
func (g gal) resolve(token string) (mid, status uint32) {
	found := -1
	for i, u := range g.users {
		if u.hidden&abHideResolve != 0 {
			continue // hidden from name resolution
		}
		if u.matchesToken(token) {
			if found >= 0 {
				return 0, midAmbiguous
			}
			found = i
		}
	}
	if found < 0 {
		return 0, midUnresolved
	}
	return g.users[found].mid, midResolved
}

// byAddress resolves an exact (case-insensitive) SMTP address to its MId — the
// reverse DNToMId applies after recovering the address from a PR_ENTRYID's DN.
func (g gal) byAddress(smtp string) (uint32, bool) {
	for _, u := range g.users {
		if strings.EqualFold(u.smtp, smtp) {
			return u.mid, true
		}
	}
	return 0, false
}

// resolveEntry returns the GAL user a STAT.cur_rec addresses for a single-entry
// fetch (GetProps). An entry MId (>= midBase) is a direct lookup; a table
// bookmark or positional value resolves by position. Because our entry MIds
// start exactly at midBase, the boundary is >= midBase (not the reference's
// hashed-minid > 0x10), so the first GAL entry routes to a direct lookup. ok is
// false when the cursor addresses no row (END_OF_TABLE, or an out-of-range MId).
func (g gal) resolveEntry(curRec uint32) (galUser, bool) {
	if curRec >= midBase {
		return g.byMID(curRec)
	}
	i := g.position(curRec)
	if i < 0 || i >= len(g.users) {
		return galUser{}, false
	}
	return g.users[i], true
}

// galUserProps projects a GAL user into the address-book property bag a row
// carries: the permanent EntryID (with the reversible DN), the display name, the
// SMTP address under the standard address tags, and the object/display types.
func galUserProps(u galUser) mapi.PropertyValues {
	return mapi.PropertyValues{
		{Tag: mapi.PrEntryID, Value: permanentEntryID(dtMailuser, userDN(u.smtp))},
		{Tag: mapi.PrDisplayName, Value: u.display},
		{Tag: mapi.PrAddrType, Value: "SMTP"},
		{Tag: mapi.PrEmailAddress, Value: u.smtp},
		{Tag: mapi.PrSmtpAddress, Value: u.smtp},
		{Tag: mapi.PrObjectType, Value: int32(mapi.ObjectTypeMailUser)},
		{Tag: mapi.PrDisplayType, Value: int32(mapi.DisplayTypeMailUser)},
	}
}
