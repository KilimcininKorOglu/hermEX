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

// galUser is one GAL entry with its assigned MId.
type galUser struct {
	mid     uint32
	display string
	smtp    string
}

// gal is the address-sorted Global Address List with MId assignment. It is
// recomputed per request (the STAT cursor is client-carried, so the server
// keeps no snapshot), and MId = midBase + index is stable as long as the GAL set
// is unchanged within a session.
type gal struct {
	users []galUser
}

// snapshot builds the GAL: every directory user, sorted by SMTP address, each
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
	sort.Slice(entries, func(i, j int) bool { return entries[i].Address < entries[j].Address })
	users := make([]galUser, len(entries))
	for i, e := range entries {
		users[i] = galUser{mid: midBase + uint32(i), display: e.DisplayName, smtp: e.Address}
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
