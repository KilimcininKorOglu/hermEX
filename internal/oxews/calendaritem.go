package oxews

import (
	"encoding/xml"
	"time"
)

// AppointmentClass is the stored message class of a calendar item.
const AppointmentClass = "IPM.Appointment"

// The CalendarItemType values ([MS-OXWSCAL] CalendarItemTypeType) this server emits.
const (
	CalendarItemSingle          = "Single"
	CalendarItemRecurringMaster = "RecurringMaster"
	CalendarItemOccurrence      = "Occurrence"
	CalendarItemException       = "Exception"
)

// CalendarItem is the EWS <t:CalendarItem> element ([MS-OXWSCAL] CalendarItemType),
// the subset this server emits. The field order IS the wire order: the type is an
// XSD sequence, the ItemType fields first and the calendar fields after them.
type CalendarItem struct {
	XMLName                    xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarItem"`
	ItemID                     ItemIDElem         `xml:"ItemId"`
	ItemClass                  string             `xml:"ItemClass,omitempty"`
	Subject                    string             `xml:"Subject,omitempty"`
	Sensitivity                string             `xml:"Sensitivity,omitempty"`
	Body                       *Body              `xml:"Body,omitempty"`
	Categories                 *Categories        `xml:"Categories,omitempty"`
	Importance                 string             `xml:"Importance,omitempty"`
	ReminderIsSet              bool               `xml:"ReminderIsSet"`
	ReminderMinutesBeforeStart *int32             `xml:"ReminderMinutesBeforeStart,omitempty"`
	HasAttachments             bool               `xml:"HasAttachments"`
	ExtendedProperties         []ExtendedProperty `xml:"ExtendedProperty"`
	UID                        string             `xml:"UID,omitempty"`
	Start                      string             `xml:"Start,omitempty"`
	End                        string             `xml:"End,omitempty"`
	OriginalStart              string             `xml:"OriginalStart,omitempty"`
	// IsAllDayEvent is always written: a client that finds it absent (eM Client)
	// drops the item from its calendar.
	IsAllDayEvent        bool          `xml:"IsAllDayEvent"`
	LegacyFreeBusyStatus string        `xml:"LegacyFreeBusyStatus,omitempty"`
	Location             string        `xml:"Location,omitempty"`
	IsMeeting            bool          `xml:"IsMeeting"`
	IsCancelled          bool          `xml:"IsCancelled"`
	IsRecurring          bool          `xml:"IsRecurring"`
	CalendarItemType     string        `xml:"CalendarItemType,omitempty"`
	MyResponseType       string        `xml:"MyResponseType,omitempty"`
	Organizer            *Recipient    `xml:"Organizer,omitempty"`
	RequiredAttendees    *AttendeeList `xml:"RequiredAttendees,omitempty"`
	OptionalAttendees    *AttendeeList `xml:"OptionalAttendees,omitempty"`
	StartTimeZone        *TimeZoneDef  `xml:"StartTimeZone,omitempty"`
	EndTimeZone          *TimeZoneDef  `xml:"EndTimeZone,omitempty"`
}

// AttendeeList is a <t:RequiredAttendees> or <t:OptionalAttendees> element.
type AttendeeList struct {
	Attendees []Attendee `xml:"Attendee"`
}

// Attendee is one <t:Attendee>: the mailbox and the answer it gave.
type Attendee struct {
	Mailbox      Mailbox `xml:"Mailbox"`
	ResponseType string  `xml:"ResponseType,omitempty"`
}

// CalendarMeta carries the appointment facts a calendar item is rendered from. The
// caller resolves them from the store's named properties, so this package stays
// free of store and named-property knowledge, as BuildMeetingRequest does.
type CalendarMeta struct {
	Subject         string
	Body            string
	BodyHTML        bool // Body is HTML rather than plain text
	Categories      []string
	Importance      *int32 // PidTagImportance, nil when unset
	Sensitivity     *int32 // PidTagSensitivity, nil when unset
	ReminderSet     bool
	ReminderMinutes *int32
	UID             string
	Start, End      time.Time
	AllDay          bool
	BusyStatus      string // LegacyFreeBusyType name
	Location        string
	Meeting         bool
	Cancelled       bool
	Recurring       bool
	// OriginalStart is the instant an occurrence of a series was generated for,
	// zero for an item that is not an occurrence.
	OriginalStart time.Time
	// Exception marks an occurrence an exception of its series changed.
	Exception  bool
	MyResponse string // ResponseTypeType name, "" when none
	Organizer  *Mailbox
	Required   []Attendee
	Optional   []Attendee
	Zone       string // the Windows time-zone id of Start and End, "" when none
}

// BuildCalendarItem renders a stored appointment as <t:CalendarItem>.
func BuildCalendarItem(meta ItemMeta, c CalendarMeta) CalendarItem {
	out := CalendarItem{
		ItemID:                     ItemIDElem{ID: meta.ItemID, ChangeKey: meta.ChangeKey},
		ItemClass:                  meta.ItemClass,
		Subject:                    c.Subject,
		ReminderIsSet:              c.ReminderSet,
		ReminderMinutesBeforeStart: c.ReminderMinutes,
		HasAttachments:             meta.HasAttachments,
		UID:                        c.UID,
		Start:                      calendarTime(c.Start),
		End:                        calendarTime(c.End),
		IsAllDayEvent:              c.AllDay,
		LegacyFreeBusyStatus:       c.BusyStatus,
		Location:                   c.Location,
		IsMeeting:                  c.Meeting,
		IsCancelled:                c.Cancelled,
		IsRecurring:                c.Recurring,
		CalendarItemType:           CalendarItemSingle,
		MyResponseType:             c.MyResponse,
		RequiredAttendees:          attendeeList(c.Required),
		OptionalAttendees:          attendeeList(c.Optional),
	}
	out.CalendarItemType = calendarItemType(c)
	out.OriginalStart = calendarTime(c.OriginalStart)
	if c.Importance != nil {
		out.Importance = importanceName(*c.Importance)
	}
	if c.Sensitivity != nil {
		out.Sensitivity = sensitivityName(*c.Sensitivity)
	}
	if c.Body != "" {
		out.Body = &Body{BodyType: "Text", Content: c.Body}
		if c.BodyHTML {
			out.Body.BodyType = "HTML"
		}
	}
	if len(c.Categories) > 0 {
		out.Categories = &Categories{String: c.Categories}
	}
	if c.Organizer != nil {
		out.Organizer = &Recipient{Mailbox: *c.Organizer}
	}
	if c.Zone != "" {
		out.StartTimeZone = &TimeZoneDef{ID: c.Zone}
		out.EndTimeZone = &TimeZoneDef{ID: c.Zone}
	}
	return out
}

// calendarItemType names what the item is within its series, if any.
func calendarItemType(c CalendarMeta) string {
	switch {
	case c.Exception:
		return CalendarItemException
	case !c.OriginalStart.IsZero():
		return CalendarItemOccurrence
	case c.Recurring:
		return CalendarItemRecurringMaster
	}
	return CalendarItemSingle
}

// calendarTime renders an instant as xs:dateTime, "" for the zero time.
func calendarTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(ewsTime)
}

// attendeeList wraps attendees, nil for none so the element is left out.
func attendeeList(a []Attendee) *AttendeeList {
	if len(a) == 0 {
		return nil
	}
	return &AttendeeList{Attendees: a}
}
