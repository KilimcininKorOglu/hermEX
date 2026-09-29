package ews

import (
	"errors"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// PidLidAppointmentStateFlags bits (MS-OXOCAL 2.2.1.10): asfMeeting marks a meeting
// rather than a plain appointment, asfCanceled one its organizer cancelled.
const (
	asfMeeting  int32 = 0x1
	asfCanceled int32 = 0x4
)

// recipOrganizer is the PidTagRecipientFlags bit (MS-OXOCAL 2.2.4.10.1) of the
// recipient row that names the organizer rather than an attendee.
const recipOrganizer int32 = 0x2

// calendarReader renders stored appointments as <t:CalendarItem>. The named
// property tags are resolved once for the whole request, not per item.
type calendarReader struct {
	st                                                  *objectstore.Store
	tags                                                meetingTagSet
	loc, recur, reminderSet, reminderDelta, resp, state mapi.PropTag
	goid                                                mapi.PropTag
}

// newCalendarReader resolves the appointment named properties. It never
// allocates: a property the mailbox never stored reads as absent.
func newCalendarReader(st *objectstore.Store) (*calendarReader, error) {
	tags, err := meetingTags(st)
	if err != nil {
		return nil, err
	}
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{
		mapi.NameAppointmentLocation,   // 0
		mapi.NameRecurring,             // 1
		mapi.NameReminderSet,           // 2
		mapi.NameReminderDelta,         // 3
		mapi.NameResponseStatus,        // 4
		mapi.NameAppointmentStateFlags, // 5
		mapi.NameGlobalObjectId,        // 6
	})
	if err != nil {
		return nil, err
	}
	return &calendarReader{
		st:            st,
		tags:          tags,
		loc:           mapi.MakeTag(ids[0], mapi.PtUnicode),
		recur:         mapi.MakeTag(ids[1], mapi.PtBoolean),
		reminderSet:   mapi.MakeTag(ids[2], mapi.PtBoolean),
		reminderDelta: mapi.MakeTag(ids[3], mapi.PtLong),
		resp:          mapi.MakeTag(ids[4], mapi.PtLong),
		state:         mapi.MakeTag(ids[5], mapi.PtLong),
		goid:          mapi.MakeTag(ids[6], mapi.PtBinary),
	}, nil
}

// item renders one stored appointment under the given item id.
func (r *calendarReader) item(id oxews.ItemID, itemID string) (oxews.CalendarItem, error) {
	msg, err := r.st.OpenMessage(id.MessageID)
	if err != nil {
		return oxews.CalendarItem{}, err
	}
	return r.render(id, itemID, msg, r.meta(msg))
}

// render builds the <t:CalendarItem> of one stored appointment from its facts.
func (r *calendarReader) render(id oxews.ItemID, itemID string, msg *oxcmail.Message, c oxews.CalendarMeta) (oxews.CalendarItem, error) {
	hasAttach, err := r.st.HasAttachments(id.MessageID)
	if err != nil {
		return oxews.CalendarItem{}, err
	}
	cats, err := r.st.GetCategories(id.MessageID)
	if err != nil {
		return oxews.CalendarItem{}, err
	}
	meta := oxews.ItemMeta{
		ItemID:         itemID,
		ChangeKey:      changeKey(r.st, id.MessageID),
		ItemClass:      itemClass(msg.Props),
		HasAttachments: hasAttach,
	}
	c.Categories = cats
	return oxews.BuildCalendarItem(meta, c), nil
}

// meta reads the appointment facts of one stored object.
func (r *calendarReader) meta(msg *oxcmail.Message) oxews.CalendarMeta {
	p := msg.Props
	start, end := r.span(p)
	flags := longProp(p, r.state)
	c := oxews.CalendarMeta{
		Subject:     strProp(p, mapi.PrSubject),
		Body:        strProp(p, mapi.PrBody),
		ReminderSet: boolProp(p, r.reminderSet),
		UID:         r.uid(p),
		Start:       start,
		End:         end,
		AllDay:      boolProp(p, r.tags.allDay),
		BusyStatus:  busyTypeName(longProp(p, r.tags.busy)),
		Location:    strProp(p, r.loc),
		Meeting:     flags&asfMeeting != 0,
		Cancelled:   flags&asfCanceled != 0,
		Recurring:   boolProp(p, r.recur),
		MyResponse:  responseTypeName(longProp(p, r.resp)),
		Zone:        meetingZone(r.st, &p),
	}
	if c.Body == "" {
		if html, ok := p.Get(mapi.PrHTML); ok {
			if b, ok := html.([]byte); ok {
				c.Body, c.BodyHTML = string(b), true
			}
		}
	}
	c.Importance = optionalLong(p, mapi.PrImportance)
	c.Sensitivity = optionalLong(p, mapi.PrSensitivity)
	if c.ReminderSet {
		c.ReminderMinutes = optionalLong(p, r.reminderDelta)
	}
	// Not every writer sets the meeting bit, so an appointment with attendees is a
	// meeting too, the rule webmail applies.
	c.Required, c.Optional = r.attendees(msg.Recipients)
	c.Meeting = c.Meeting || len(c.Required)+len(c.Optional) > 0
	if c.Meeting {
		c.Organizer = organizerMailbox(p)
	}
	return c
}

// span reads the appointment's start and end. A series whose body is kept
// verbatim is read from that body, as ActiveSync reads it, because a series stored
// before its end was recorded carries only its start.
func (r *calendarReader) span(p mapi.PropertyValues) (start, end time.Time) {
	start, end = ntTime(p, r.tags.start), ntTime(p, r.tags.end)
	if v, ok := p.Get(mapi.PrIcalOriginal); ok {
		if ical, ok := v.([]byte); ok && len(ical) > 0 {
			if s, e, _, ok := oxcical.ParseRecurrence(ical); ok {
				return s.UTC(), e.UTC()
			}
		}
	}
	return start, end
}

// uid is the iCalendar UID the appointment exports with: the preserved one, else
// the one its global object id names.
func (r *calendarReader) uid(p mapi.PropertyValues) string {
	if uid := strProp(p, r.tags.uid); uid != "" {
		return uid
	}
	if v, ok := p.Get(r.goid); ok {
		if goid, ok := v.([]byte); ok {
			return oxcical.GlobalObjectUID(goid)
		}
	}
	return ""
}

// attendees splits a meeting's recipient rows into required and optional
// attendees, leaving out the row that names the organizer. A Cc row is an
// optional attendee ([MS-OXCICAL] 2.1.3.1.1.20.2); a Bcc row is a resource and is
// not an attendee.
func (r *calendarReader) attendees(rows []mapi.PropertyValues) (required, optional []oxews.Attendee) {
	for _, row := range rows {
		if longProp(row, mapi.PrRecipientFlags)&recipOrganizer != 0 {
			continue
		}
		addr := strProp(row, mapi.PrSmtpAddress)
		if addr == "" {
			addr = strProp(row, mapi.PrEmailAddress)
		}
		if addr == "" {
			continue
		}
		a := oxews.Attendee{
			Mailbox:      oxews.Mailbox{Name: strProp(row, mapi.PrDisplayName), EmailAddress: addr},
			ResponseType: attendeeResponse(longProp(row, r.resp)),
		}
		switch longProp(row, mapi.PrRecipientType) {
		case mapi.RecipCc:
			optional = append(optional, a)
		case mapi.RecipBcc:
		default:
			required = append(required, a)
		}
	}
	return required, optional
}

// responseTypeName maps a PidLidResponseStatus to its ResponseTypeType name
// ([MS-OXWSCDATA] 2.2.5.28), "" for none so the element is left out.
func responseTypeName(status int32) string {
	switch status {
	case 1:
		return "Organizer"
	case 2:
		return "Tentative"
	case 3:
		return "Accept"
	case 4:
		return "Decline"
	case 5:
		return "NoResponseReceived"
	}
	return ""
}

// attendeeResponse names an attendee's answer, Unknown when the row records none.
func attendeeResponse(status int32) string {
	if name := responseTypeName(status); name != "" {
		return name
	}
	return "Unknown"
}

// optionalLong reads a PtLong property, nil when absent.
func optionalLong(p mapi.PropertyValues, tag mapi.PropTag) *int32 {
	if _, ok := p.Get(tag); !ok {
		return nil
	}
	v := longProp(p, tag)
	return &v
}

// isCalendarFolder reports whether a folder holds calendar items: the Calendar
// folder itself, or any folder whose container class is IPF.Appointment. Its items
// live in the object store only, never in the IMAP index.
func isCalendarFolder(st *objectstore.Store, fid int64) (bool, error) {
	if fid == int64(mapi.PrivateFIDCalendar) {
		return true, nil
	}
	props, err := st.GetFolderProperties(fid, mapi.PrContainerClass)
	if errors.Is(err, objectstore.ErrNotFound) {
		return false, nil // the folder listing reports the missing folder
	}
	if err != nil {
		return false, err
	}
	class := strProp(props, mapi.PrContainerClass)
	return class == mapi.ContainerClassAppointment || strings.HasPrefix(class, mapi.ContainerClassAppointment+"."), nil
}

// calendar lists a calendar folder as calendar items.
func (l itemListing) calendar(fid int64) findItemResponseMessage {
	if l.view != nil {
		return l.calendarView(fid)
	}
	objs, matched, code := l.objects(fid)
	if code != "" {
		return findItemError(code)
	}
	reader, err := newCalendarReader(l.st)
	if err != nil {
		return findItemError("ErrorInternalServerError")
	}
	items := make([]oxews.CalendarItem, 0, len(objs))
	for _, o := range objs {
		if !kept(matched, o.ID) {
			continue
		}
		id := oxews.ItemID{FolderID: fid, MessageID: o.ID, Mailbox: l.idMailbox}
		item, err := reader.item(id, oxews.EncodeItemID(id))
		if err != nil {
			return findItemError("ErrorInternalServerError")
		}
		item.ExtendedProperties = readExtended(l.st, o.ID, l.fields)
		items = append(items, item)
	}
	return findItemFound(&findItemRoot{
		TotalItemsInView:        len(items),
		IncludesLastItemInRange: true,
		Items:                   itemsWrap{CalendarItems: items},
	})
}

// calendarItemResponse answers GetItem for one stored appointment.
func calendarItemResponse(st *objectstore.Store, id oxews.ItemID, itemID string, fields []extField) itemResponseMessage {
	reader, err := newCalendarReader(st)
	if err != nil {
		return itemError("ErrorInternalServerError")
	}
	read := reader.item
	if id.Instance != 0 {
		read = reader.occurrence
	}
	item, err := read(id, itemID)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}
	item.ExtendedProperties = readExtended(st, id.MessageID, fields)
	return itemFound(&itemsWrap{CalendarItems: []oxews.CalendarItem{item}})
}

// isAppointment reports whether a stored class is a calendar item.
func isAppointment(class string) bool {
	return class == oxews.AppointmentClass || strings.HasPrefix(class, oxews.AppointmentClass+".")
}
