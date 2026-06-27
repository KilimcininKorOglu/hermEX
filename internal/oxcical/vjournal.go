package oxcical

import (
	"errors"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

var errNoJournal = errors.New("oxcical: no VJOURNAL in calendar")

// ImportVJournal parses an iCalendar VJOURNAL into an IPM.Activity (journal) message.
// The verbatim source is preserved in PrIcalOriginal and served back unchanged on
// export; the UID, SUMMARY, and DESCRIPTION are mirrored into MAPI properties so the
// item lists and searches like any other store object (single-data, no separate model).
func ImportVJournal(raw []byte, opt Options) (*oxcmail.Message, error) {
	cal, err := parseICal(raw)
	if err != nil {
		return nil, err
	}
	vj := cal.sub("VJOURNAL")
	if vj == nil {
		return nil, errNoJournal
	}
	uidTag, err := resolveOne(opt, nameICalUID, mapi.PtUnicode, true)
	if err != nil {
		return nil, err
	}
	msg := &oxcmail.Message{}
	p := &msg.Props
	p.Set(mapi.PrMessageClass, "IPM.Activity")
	uid := strings.TrimSpace(vj.propText("UID"))
	if uid == "" {
		uid = generatedUID(vj)
	}
	if uidTag != 0 {
		p.Set(uidTag, uid)
	}
	setIf(p, mapi.PrSubject, vj.propText("SUMMARY"))
	setIf(p, mapi.PrBody, vj.propText("DESCRIPTION"))
	p.Set(mapi.PrIcalOriginal, append([]byte(nil), raw...))
	return msg, nil
}

// ExportVJournal renders a stored journal message back to its iCalendar VJOURNAL. The
// verbatim source preserved on import is returned unchanged; absent it, a minimal
// VJOURNAL is built from the listing properties under the given UID.
func ExportVJournal(msg *oxcmail.Message, uid string) []byte {
	if v, ok := msg.Props.Get(mapi.PrIcalOriginal); ok {
		if b, ok := v.([]byte); ok && len(b) > 0 {
			return b
		}
	}
	str := func(tag mapi.PropTag) string {
		if v, ok := msg.Props.Get(tag); ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	b := &builder{}
	b.add("BEGIN:VCALENDAR")
	b.add("VERSION:2.0")
	b.add("PRODID:-//hermEX//CalDAV//EN")
	b.add("BEGIN:VJOURNAL")
	b.line("UID", uid)
	if s := str(mapi.PrSubject); s != "" {
		b.line("SUMMARY", s)
	}
	if s := str(mapi.PrBody); s != "" {
		b.line("DESCRIPTION", s)
	}
	b.add("END:VJOURNAL")
	b.add("END:VCALENDAR")
	return b.buf.Bytes()
}
