package ews

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxews"
)

// defaultReminderMinutes is the reminder Exchange sets on a calendar item whose
// request says nothing about one.
const defaultReminderMinutes = 15

// createCalendarItem is one <t:CalendarItem> of a CreateItem ([MS-OXWSCAL]
// CalendarItemType), the fields this server stores. Every other element lands in
// Other, and one that is not known to be safe to leave out refuses the item, so
// nothing the client set is dropped without a word.
type createCalendarItem struct {
	Subject string `xml:"Subject"`
	Body    struct {
		Type    string `xml:"BodyType,attr"`
		Content string `xml:",chardata"`
	} `xml:"Body"`
	Categories struct {
		String []string `xml:"String"`
	} `xml:"Categories"`
	Importance           string                   `xml:"Importance"`
	Sensitivity          string                   `xml:"Sensitivity"`
	ReminderIsSet        *bool                    `xml:"ReminderIsSet"`
	ReminderMinutes      *int                     `xml:"ReminderMinutesBeforeStart"`
	Extended             []oxews.ExtendedProperty `xml:"ExtendedProperty"`
	UID                  string                   `xml:"UID"`
	Start                string                   `xml:"Start"`
	End                  string                   `xml:"End"`
	IsAllDayEvent        bool                     `xml:"IsAllDayEvent"`
	LegacyFreeBusyStatus string                   `xml:"LegacyFreeBusyStatus"`
	Location             string                   `xml:"Location"`
	RequiredAttendees    attendeeRefs             `xml:"RequiredAttendees"`
	OptionalAttendees    attendeeRefs             `xml:"OptionalAttendees"`
	Resources            attendeeRefs             `xml:"Resources"`
	Recurrence           *recurrenceRequest       `xml:"Recurrence"`
	StartTimeZone        *zoneRef                 `xml:"StartTimeZone"`
	EndTimeZone          *zoneRef                 `xml:"EndTimeZone"`
	MeetingTimeZone      *struct {
		Name string `xml:"TimeZoneName,attr"`
	} `xml:"MeetingTimeZone"`
	Other []struct {
		XMLName xml.Name
	} `xml:",any"`
}

// zoneRef is a <t:StartTimeZone> or <t:EndTimeZone>, named by its Windows id.
type zoneRef struct {
	ID string `xml:"Id,attr"`
}

// attendeeRefs is a <t:RequiredAttendees>, <t:OptionalAttendees> or
// <t:Resources> list.
type attendeeRefs struct {
	Attendee []struct {
		Mailbox mailboxEntry `xml:"Mailbox"`
	} `xml:"Attendee"`
}

// ignorableCalendarFields are the CalendarItem elements clients send that carry
// nothing this server stores apart from what it derives itself.
var ignorableCalendarFields = map[string]bool{
	"ItemClass": true, "ReminderDueBy": true, "IsResponseRequested": true,
	"AllowNewTimeProposal": true, "ConferenceType": true, "IsOnlineMeeting": true,
	"Culture": true, "MeetingWorkspaceUrl": true, "NetShowUrl": true,
}

// busyStatusValues maps LegacyFreeBusyType ([MS-OXWSCDATA] 2.2.5.19) to
// PidLidBusyStatus.
var busyStatusValues = map[string]int32{
	"Free": 0, "Tentative": 1, "Busy": 2, "OOF": 3, "WorkingElsewhere": 4,
}

// importanceValues and sensitivityValues map the EWS names to their MAPI values.
var (
	importanceValues  = map[string]int32{"Low": 0, "Normal": 1, "High": 2}
	sensitivityValues = map[string]int32{"Normal": 0, "Personal": 1, "Private": 2, "Confidential": 3}
)

// errCalendarField reports a CalendarItem element this server would drop.
var errCalendarField = errors.New("ews: an unsupported calendar item field")

// calendarCreate is one calendar item to store: the request's item and the
// mailbox and folder it goes to.
type calendarCreate struct {
	st          *objectstore.Store
	fid         int64
	organizer   string
	invitations string // SendMeetingInvitations
	item        createCalendarItem
}

// createCalendarItems stores every calendar item in the request.
func (s *Server) createCalendarItems(st *objectstore.Store, sess *session, req createItemRequest) []itemResponseMessage {
	if len(req.Items.CalendarItems) == 0 {
		return nil
	}
	fid, code := calendarSaveFolder(st, req.SavedItemFolderID)
	var msgs []itemResponseMessage
	for _, item := range req.Items.CalendarItems {
		switch {
		case code != "":
			msgs = append(msgs, itemError(code))
		case req.SendMeetingInvitations == "":
			msgs = append(msgs, itemError("ErrorSendMeetingInvitationsRequired"))
		default:
			c := calendarCreate{st: st, fid: fid, organizer: sess.user, invitations: req.SendMeetingInvitations, item: item}
			msgs = append(msgs, s.createOneCalendarItem(c))
		}
	}
	return msgs
}

// calendarSaveFolder is the calendar a created item is stored in: the one the
// request names, which must be a calendar of the caller's own mailbox, else the
// default Calendar.
func calendarSaveFolder(st *objectstore.Store, refs folderRefs) (int64, string) {
	targets := resolveTargets(refs)
	if len(targets) == 0 {
		return int64(mapi.PrivateFIDCalendar), ""
	}
	t := targets[0]
	if !t.ok || t.mailbox != "" {
		return 0, "ErrorFolderNotFound"
	}
	calendar, err := isCalendarFolder(st, t.fid)
	if err != nil {
		return 0, "ErrorInternalServerError"
	}
	if !calendar {
		return 0, "ErrorInvalidRequest"
	}
	return t.fid, ""
}

// createOneCalendarItem stores one calendar item through the iCalendar import
// every protocol's appointments are stored with, then sends its invitations as
// the request asks.
func (s *Server) createOneCalendarItem(c calendarCreate) itemResponseMessage {
	ical, code := c.item.iCalendar(c.meetingOrganizer())
	if code != "" {
		return itemError(code)
	}
	after, code := c.item.storedProps(c.st)
	if code != "" {
		return itemError(code)
	}
	msg, err := oxcical.Import(ical, oxcical.Options{Resolver: c.st.GetNamedPropIDs})
	if err != nil {
		return itemError("ErrorCalendarInvalidRecurrence")
	}
	id, err := c.st.CreateMessage(c.fid, msg)
	if err != nil {
		return itemError("ErrorItemSave")
	}
	if err := c.finish(id, after); err != nil {
		c.st.LogSwallowedError("ews.calendar_create", err)
	}
	s.inviteAttendees(c, id)
	itemID := oxews.EncodeItemID(oxews.ItemID{FolderID: c.fid, MessageID: id})
	return itemFound(&itemsWrap{CalendarItems: []oxews.CalendarItem{{
		ItemID: oxews.ItemIDElem{ID: itemID, ChangeKey: changeKey(c.st, id)},
	}}})
}

// meetingOrganizer is the organizer the item records: the caller when it names
// anyone to meet, none for an appointment.
func (c calendarCreate) meetingOrganizer() string {
	if len(c.item.attendees()) == 0 {
		return ""
	}
	return c.organizer
}

// finish writes what the iCalendar import cannot carry: the busy status, the
// importance, the categories, an HTML body and the extended properties. The item
// is stored by then, so a failure is recorded rather than reported as a failed
// create, which a client would retry into a duplicate.
func (c calendarCreate) finish(id int64, props mapi.PropertyValues) error {
	if cats := c.item.Categories.String; len(cats) > 0 {
		if err := c.st.SetCategories(id, cats); err != nil {
			return err
		}
	}
	if len(props) == 0 {
		return nil
	}
	return c.st.ModifyMessageProperties(id, props)
}

// storedProps are the properties set on the stored item beside the import. A
// value the request names that has no MAPI meaning refuses the item.
func (item createCalendarItem) storedProps(st *objectstore.Store) (mapi.PropertyValues, string) {
	props, code := extendedValues(st, item.Extended)
	if code != "" {
		return nil, code
	}
	if err := item.namedProps(st, &props); err != nil {
		return nil, "ErrorInvalidPropertySet"
	}
	if v, ok := importanceValues[item.Importance]; ok {
		props.Set(mapi.PrImportance, v)
	} else if item.Importance != "" {
		return nil, "ErrorInvalidPropertySet"
	}
	if strings.EqualFold(item.Body.Type, "HTML") && item.Body.Content != "" {
		props.Set(mapi.PrHTML, []byte(oxews.ToCRLF(item.Body.Content)))
	}
	return props, ""
}

// namedProps sets the busy status and the invitation flag, the two named
// properties the stored item takes from the request.
func (item createCalendarItem) namedProps(st *objectstore.Store, props *mapi.PropertyValues) error {
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameBusyStatus, mapi.NameFInvited})
	if err != nil {
		return err
	}
	if item.LegacyFreeBusyStatus != "" {
		v, ok := busyStatusValues[item.LegacyFreeBusyStatus]
		if !ok {
			return errCalendarField
		}
		props.Set(mapi.MakeTag(ids[0], mapi.PtLong), v)
	}
	if len(item.attendees()) > 0 {
		props.Set(mapi.MakeTag(ids[1], mapi.PtBoolean), false)
	}
	return nil
}

// inviteAttendees sends a meeting's request to its attendees when the request
// asks for it, and marks the meeting invited. The meeting is stored by then, so a
// failed delivery is recorded and the item stands, as webmail's does.
func (s *Server) inviteAttendees(c calendarCreate, id int64) {
	to := c.item.attendees()
	if len(to) == 0 || c.invitations == "SendToNone" {
		return
	}
	m, err := invitation(c.st, id, c.organizer, to)
	if err != nil {
		c.st.LogSwallowedError("ews.meeting_invitation", err)
		return
	}
	if !s.sendScheduling(c.st, m, c.invitations) {
		return
	}
	if err := markSent(c.st, id); err != nil {
		c.st.LogSwallowedError("ews.meeting_invited", err)
	}
}

// invitation renders the stored meeting as the METHOD:REQUEST its attendees
// receive: the whole object, series rule and time zones included.
func invitation(st *objectstore.Store, id int64, organizer string, to []string) (*schedulingMail, error) {
	msg, err := st.OpenMessage(id)
	if err != nil {
		return nil, err
	}
	ical, err := oxcical.Export(msg, oxcical.Options{Resolver: st.GetNamedPropIDs})
	if err != nil {
		return nil, err
	}
	req, ok := oxcical.WithMethod(ical, "REQUEST")
	if !ok {
		return nil, errors.New("ews: the meeting does not render as a request")
	}
	return &schedulingMail{organizer: organizer, to: to, subject: strProp(msg.Props, mapi.PrSubject), method: "REQUEST", calendar: req}, nil
}

// markSent records on the stored meeting that its request went out
// (PidLidFInvited), so deleting it later tells its attendees.
func markSent(st *objectstore.Store, id int64) error {
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameFInvited})
	if err != nil {
		return err
	}
	var props mapi.PropertyValues
	props.Set(mapi.MakeTag(ids[0], mapi.PtBoolean), true)
	return st.ModifyMessageProperties(id, props)
}

// attendees lists every attendee address that parses, lowercased and once each.
func (item createCalendarItem) attendees() []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range []attendeeRefs{item.RequiredAttendees, item.OptionalAttendees, item.Resources} {
		for _, a := range list.Attendee {
			addr, ok := cleanAddress(a.Mailbox.EmailAddress)
			if ok && !seen[addr] {
				seen[addr] = true
				out = append(out, addr)
			}
		}
	}
	return out
}

// cleanAddress reduces an address to its bare lowercased form. One that does not
// parse is refused: it would reach an ATTENDEE line and a To header, so a value
// holding a line break would splice lines of the sender's choosing into both.
func cleanAddress(s string) (string, bool) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil || parsed.Address == "" {
		return "", false
	}
	return strings.ToLower(parsed.Address), true
}

// checkFields refuses an element this server would drop.
func (item createCalendarItem) checkFields() error {
	for _, o := range item.Other {
		if !ignorableCalendarFields[o.XMLName.Local] {
			return fmt.Errorf("%w: %s", errCalendarField, o.XMLName.Local)
		}
	}
	if _, ok := sensitivityValues[item.Sensitivity]; item.Sensitivity != "" && !ok {
		return errCalendarField
	}
	return nil
}

// newCalendarUID mints the iCalendar UID of an item the client named none for.
func newCalendarUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + "@hermex"
}
