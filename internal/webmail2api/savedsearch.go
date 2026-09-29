package webmail2api

import (
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// A saved search's fields become the restriction of a store search folder, and a
// restriction in that shape reads back as the same fields. Every field is one term
// of an AND: the originator, the subject and the body are case-insensitive
// substring tests, the day range bounds the delivery time, and the attachment
// field tests the paperclip bit of the message flags.

// searchFuzzyContains is a case-insensitive substring match (MS-OXCDATA 2.12.3).
const searchFuzzyContains = 0x00000001 | 0x00010000

// savedSearchRestriction builds the restriction a saved search's fields name. Its
// day bounds are whole days in loc.
func savedSearchRestriction(in searchFolderJSON, loc *time.Location) mapi.Restriction {
	var terms []mapi.Restriction
	if in.From != "" {
		terms = append(terms, objectstore.RuleAny(
			containsTerm(mapi.PrSentRepresentingName, in.From),
			containsTerm(mapi.PrSentRepresentingSmtpAddress, in.From)))
	}
	if in.Subject != "" {
		terms = append(terms, containsTerm(mapi.PrSubject, in.Subject))
	}
	if in.Body != "" {
		terms = append(terms, containsTerm(mapi.PrBody, in.Body))
	}
	if d := parseSearchDate(in.DateFrom, loc); !d.IsZero() {
		terms = append(terms, deliveredTerm(mapi.RelopGE, d))
	}
	if d := parseSearchDate(in.DateTo, loc); !d.IsZero() {
		terms = append(terms, deliveredTerm(mapi.RelopLT, d.AddDate(0, 0, 1)))
	}
	if in.HasAttachment {
		terms = append(terms, mapi.Restriction{Type: mapi.ResBitmask, Value: mapi.BitmaskRestriction{
			Relop: mapi.BmrNez, PropTag: mapi.PrMessageFlags, Mask: uint32(mapi.MsgFlagHasAttach),
		}})
	}
	return objectstore.RuleAll(terms...)
}

// containsTerm is a case-insensitive substring test of a string property.
func containsTerm(tag mapi.PropTag, text string) mapi.Restriction {
	return mapi.Restriction{Type: mapi.ResContent, Value: mapi.ContentRestriction{
		FuzzyLevel: searchFuzzyContains,
		PropTag:    tag,
		PropVal:    mapi.TaggedPropVal{Tag: tag, Value: text},
	}}
}

// deliveredTerm compares the delivery time against at.
func deliveredTerm(op mapi.Relop, at time.Time) mapi.Restriction {
	return mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
		Relop:   op,
		PropTag: mapi.PrMessageDeliveryTime,
		PropVal: mapi.TaggedPropVal{Tag: mapi.PrMessageDeliveryTime, Value: mapi.UnixToNTTime(at)},
	}}
}

// readSavedSearch fills a saved search's fields from a restriction in the shape
// savedSearchRestriction builds, reporting false for any other shape, which a
// client other than webmail wrote. Its days are days in loc.
func readSavedSearch(r mapi.Restriction, out *searchFolderJSON, loc *time.Location) bool {
	terms, ok := r.Value.([]mapi.Restriction)
	if r.Type != mapi.ResAnd || !ok {
		return false
	}
	for _, t := range terms {
		if !readSearchTerm(t, out, loc) {
			return false
		}
	}
	return true
}

// readSearchTerm fills the field one term of the AND names.
func readSearchTerm(t mapi.Restriction, out *searchFolderJSON, loc *time.Location) bool {
	switch v := t.Value.(type) {
	case mapi.ContentRestriction:
		return readContainsTerm(v, out)
	case []mapi.Restriction:
		return t.Type == mapi.ResOr && readFromTerm(v, out)
	case mapi.PropertyRestriction:
		return readDeliveredTerm(v, out, loc)
	case mapi.BitmaskRestriction:
		out.HasAttachment = v.Relop == mapi.BmrNez && v.PropTag == mapi.PrMessageFlags && v.Mask == uint32(mapi.MsgFlagHasAttach)
		return out.HasAttachment
	}
	return false
}

// readContainsTerm reads a subject or body term.
func readContainsTerm(c mapi.ContentRestriction, out *searchFolderJSON) bool {
	text, ok := containsText(c)
	switch {
	case !ok:
		return false
	case c.PropTag == mapi.PrSubject:
		out.Subject = text
	case c.PropTag == mapi.PrBody:
		out.Body = text
	default:
		return false
	}
	return true
}

// readFromTerm reads the originator term: the same text tested against the
// sent-representing name and address.
func readFromTerm(kids []mapi.Restriction, out *searchFolderJSON) bool {
	if len(kids) != 2 {
		return false
	}
	name, ok1 := kids[0].Value.(mapi.ContentRestriction)
	addr, ok2 := kids[1].Value.(mapi.ContentRestriction)
	if !ok1 || !ok2 || name.PropTag != mapi.PrSentRepresentingName || addr.PropTag != mapi.PrSentRepresentingSmtpAddress {
		return false
	}
	n, okn := containsText(name)
	a, oka := containsText(addr)
	if !okn || !oka || n != a {
		return false
	}
	out.From = n
	return true
}

// containsText returns the text of a case-insensitive substring term.
func containsText(c mapi.ContentRestriction) (string, bool) {
	text, ok := c.PropVal.Value.(string)
	return text, ok && c.FuzzyLevel == searchFuzzyContains
}

// readDeliveredTerm reads a day bound: the start of the from day, or the start of
// the day after the to day.
func readDeliveredTerm(p mapi.PropertyRestriction, out *searchFolderJSON, loc *time.Location) bool {
	nt, ok := p.PropVal.Value.(uint64)
	if !ok || p.PropTag != mapi.PrMessageDeliveryTime {
		return false
	}
	at := mapi.NTTimeToUnix(nt).In(loc)
	switch p.Relop {
	case mapi.RelopGE:
		out.DateFrom = at.Format(time.DateOnly)
	case mapi.RelopLT:
		out.DateTo = at.AddDate(0, 0, -1).Format(time.DateOnly)
	default:
		return false
	}
	return true
}

// parseSearchDate parses a YYYY-MM-DD day as its start in loc, or an RFC3339
// date, returning the zero time when empty or unparseable.
func parseSearchDate(s string, loc *time.Location) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.ParseInLocation(time.DateOnly, s, loc); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}
