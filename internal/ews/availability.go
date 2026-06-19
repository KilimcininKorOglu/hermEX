package ews

import (
	"encoding/xml"
	"net/http"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// --- request types ---
//
// Element names are matched on their local name only (Go ignores the namespace
// when a struct tag carries none), so the t:/m: prefixes the client sends do not
// need spelling out here.

type getUserAvailabilityRequest struct {
	// TimeZone presence is required: the reference rejects a request without it,
	// reporting ErrorTimeZone per mailbox. v1 reads only its presence — the bias is
	// not applied to the window (see freeBusyForTarget), a documented gap.
	TimeZone         *serializableTimeZone `xml:"TimeZone"`
	MailboxDataArray struct {
		Items []mailboxData `xml:"MailboxData"`
	} `xml:"MailboxDataArray"`
	FreeBusyViewOptions *freeBusyViewOptions `xml:"FreeBusyViewOptions"`
}

type serializableTimeZone struct {
	Bias int `xml:"Bias"`
}

type mailboxData struct {
	Email struct {
		Address string `xml:"Address"`
	} `xml:"Email"`
}

type freeBusyViewOptions struct {
	TimeWindow struct {
		StartTime string `xml:"StartTime"`
		EndTime   string `xml:"EndTime"`
	} `xml:"TimeWindow"`
	// RequestedView is parsed but ignored: the reference derives the detail level
	// from the caller's calendar permission, never from the requested view.
	RequestedView string `xml:"RequestedView"`
}

// --- response types ---
//
// The root sets the messages namespace as the default; unqualified children
// inherit it (matching the reference's XMLDUMPM). The free/busy view's leaves
// switch to the types namespace, and everything under CalendarEventArray inherits
// types in turn.

type getUserAvailabilityResponse struct {
	XMLName   xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetUserAvailabilityResponse"`
	Responses []freeBusyResponse `xml:"FreeBusyResponseArray>FreeBusyResponse"`
}

type freeBusyResponse struct {
	ResponseMessage availResponseMessage `xml:"ResponseMessage"`
	FreeBusyView    *freeBusyView        `xml:"FreeBusyView,omitempty"`
}

type availResponseMessage struct {
	ResponseClass string `xml:"ResponseClass,attr"`
	ResponseCode  string `xml:"ResponseCode"`
}

type freeBusyView struct {
	ViewType string              `xml:"http://schemas.microsoft.com/exchange/services/2006/types FreeBusyViewType"`
	Events   *calendarEventArray `xml:"http://schemas.microsoft.com/exchange/services/2006/types CalendarEventArray"`
}

type calendarEventArray struct {
	Events []calendarEvent `xml:"CalendarEvent"`
}

type calendarEvent struct {
	StartTime string                `xml:"StartTime"`
	EndTime   string                `xml:"EndTime"`
	BusyType  string                `xml:"BusyType"`
	Details   *calendarEventDetails `xml:"CalendarEventDetails,omitempty"`
}

type calendarEventDetails struct {
	Subject       string `xml:"Subject,omitempty"`
	Location      string `xml:"Location,omitempty"`
	IsMeeting     bool   `xml:"IsMeeting"`
	IsRecurring   bool   `xml:"IsRecurring"`
	IsException   bool   `xml:"IsException"`
	IsReminderSet bool   `xml:"IsReminderSet"`
	IsPrivate     bool   `xml:"IsPrivate"`
}

// freeBusyPerms is the rights mask that grants any free/busy visibility; a zero
// intersection means the caller may not read the target's free/busy at all.
const freeBusyPerms = mapi.FrightsFreeBusySimple | mapi.FrightsFreeBusyDetailed | mapi.FrightsReadAny

// freeBusyDetailPerms is the subset of free/busy rights that additionally grant
// the appointment detail (subject/location) shown in a "Detailed" view.
const freeBusyDetailPerms = mapi.FrightsFreeBusyDetailed | mapi.FrightsReadAny

// --- handler ---

// handleGetUserAvailability answers GetUserAvailability (MS-OXWSAVAIL): for each
// requested mailbox it returns the appointments overlapping the time window as a
// free/busy view, gated by the caller's permission on the target's calendar. Like
// the reference it reports per-mailbox failures (no permission, unknown user)
// through the response array rather than failing the whole request; only a missing
// view-options or time-zone element is a request-level fault.
func (s *Server) handleGetUserAvailability(w http.ResponseWriter, inner []byte, sess *session) {
	var req getUserAvailabilityRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetUserAvailability: "+err.Error())
		return
	}
	if req.FreeBusyViewOptions == nil {
		// v1 serves free/busy only; a suggestions-only request is unsupported.
		writeSOAPFault(w, "ErrorInvalidFreeBusyViewType", "GetUserAvailability: FreeBusyViewOptions is required")
		return
	}

	// A missing time zone is not fatal: every mailbox reports ErrorTimeZone,
	// matching the reference (which fills the whole array with the error).
	if req.TimeZone == nil {
		resp := getUserAvailabilityResponse{}
		for range req.MailboxDataArray.Items {
			resp.Responses = append(resp.Responses, errorFreeBusy("ErrorTimeZone"))
		}
		writeResponse(w, resp)
		return
	}

	windowStart, okS := parseAvailabilityTime(req.FreeBusyViewOptions.TimeWindow.StartTime)
	windowEnd, okE := parseAvailabilityTime(req.FreeBusyViewOptions.TimeWindow.EndTime)
	if !okS || !okE {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetUserAvailability: malformed TimeWindow")
		return
	}

	resp := getUserAvailabilityResponse{}
	for _, md := range req.MailboxDataArray.Items {
		resp.Responses = append(resp.Responses, s.freeBusyForTarget(sess, md.Email.Address, windowStart, windowEnd))
	}
	writeResponse(w, resp)
}

// freeBusyForTarget computes one mailbox's free/busy response. The caller's detail
// level follows the reference: the mailbox owner querying their own calendar always
// gets the detailed view (the reference passes a null actor, granting it implicitly),
// while another caller is held to the free/busy rights granted on the target's
// calendar — none of those rights means the data is denied, not shown as all-free.
func (s *Server) freeBusyForTarget(sess *session, email string, windowStart, windowEnd time.Time) freeBusyResponse {
	targetPath, ok := s.accounts.Resolve(email)
	if !ok {
		return errorFreeBusy("ErrorMailRecipientNotFound")
	}
	st, err := objectstore.Open(targetPath)
	if err != nil {
		return errorFreeBusy("ErrorFreeBusyGenerationFailed")
	}
	defer st.Close()

	// An owner querying their own calendar gets implicit full (detailed) access.
	// Match on the address as well as the resolved path: Authenticate (which set
	// sess.mailbox) and Resolve are separate directory lookups that may normalize
	// differently, and a client may list the requester's own address among the
	// attendees — the address compare cannot over-grant (it is the same identity)
	// and catches a path divergence the path compare would miss.
	owner := strings.EqualFold(email, sess.user) || targetPath == sess.mailbox

	var detailed bool
	if owner {
		detailed = true
	} else {
		perm, err := st.ResolvePermission(int64(mapi.PrivateFIDCalendar), sess.user)
		if err != nil {
			return errorFreeBusy("ErrorFreeBusyGenerationFailed")
		}
		if perm&freeBusyPerms == 0 {
			return errorFreeBusy("ErrorFreeBusyGenerationFailed")
		}
		detailed = perm&freeBusyDetailPerms != 0
	}

	events, err := calendarFreeBusy(st, windowStart, windowEnd, detailed)
	if err != nil {
		return errorFreeBusy("ErrorFreeBusyGenerationFailed")
	}
	// The reference computes std::all_of(has_details) over the events, which is the
	// uniform detail flag — except an empty calendar yields "Detailed" (all_of over
	// an empty range is true), a wire quirk preserved here.
	viewType := "FreeBusy"
	if detailed || len(events) == 0 {
		viewType = "Detailed"
	}
	return freeBusyResponse{
		ResponseMessage: availResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"},
		FreeBusyView: &freeBusyView{
			ViewType: viewType,
			Events:   &calendarEventArray{Events: events},
		},
	}
}

// calendarFreeBusy enumerates the target's calendar for appointments overlapping
// the window. A recurring series master is skipped: its stored start/end describe
// only the first instance, so emitting it would place a misleading single block at
// the series origin — recurrence expansion is a documented v1 gap. Detail fields
// are attached only when the caller is entitled to the detailed view.
func calendarFreeBusy(st *objectstore.Store, windowStart, windowEnd time.Time, detailed bool) ([]calendarEvent, error) {
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{
		mapi.NameAppointmentStartWhole,
		mapi.NameAppointmentEndWhole,
		mapi.NameBusyStatus,
		mapi.NameRecurring,
		mapi.NameAppointmentLocation,
		mapi.NameReminderSet,
	})
	if err != nil {
		return nil, err
	}
	startTag := namedTag(ids[0], mapi.PtSysTime)
	endTag := namedTag(ids[1], mapi.PtSysTime)
	busyTag := namedTag(ids[2], mapi.PtLong)
	recurTag := namedTag(ids[3], mapi.PtBoolean)
	locTag := namedTag(ids[4], mapi.PtUnicode)
	reminderTag := namedTag(ids[5], mapi.PtBoolean)

	// A mailbox that has never stored an appointment has no start/end named id and
	// thus no appointments to report.
	if startTag == 0 || endTag == 0 {
		return nil, nil
	}

	// Calendar items are created with CreateMessage and never enter the IMAP index,
	// so they are read from the object store directly (ListMessages would miss them).
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDCalendar))
	if err != nil {
		return nil, err
	}
	var events []calendarEvent
	for _, obj := range objs {
		pv, err := st.GetMessageProperties(obj.ID, startTag, endTag, busyTag, recurTag, locTag, reminderTag, mapi.PrSubject)
		if err != nil {
			continue
		}
		start, ok1 := ntTimeVal(pv, startTag)
		end, ok2 := ntTimeVal(pv, endTag)
		if !ok1 || !ok2 {
			continue
		}
		if boolVal(pv, recurTag) {
			continue // recurring master (v1 gap: no instance expansion)
		}
		// Overlap, not containment: an appointment that starts before the window and
		// ends after it still occupies the whole window and must appear.
		if !start.Before(windowEnd) || !end.After(windowStart) {
			continue
		}
		ev := calendarEvent{
			StartTime: start.UTC().Format(time.RFC3339),
			EndTime:   end.UTC().Format(time.RFC3339),
			BusyType:  busyTypeName(longVal(pv, busyTag)),
		}
		if detailed {
			ev.Details = &calendarEventDetails{
				Subject:       strVal(pv, mapi.PrSubject),
				Location:      strVal(pv, locTag),
				IsReminderSet: boolVal(pv, reminderTag),
			}
		}
		events = append(events, ev)
	}
	return events, nil
}

// --- helpers ---

// errorFreeBusy builds a per-mailbox error response carrying only the response
// message (no free/busy view), as the reference does on a per-mailbox failure.
func errorFreeBusy(code string) freeBusyResponse {
	return freeBusyResponse{ResponseMessage: availResponseMessage{ResponseClass: "Error", ResponseCode: code}}
}

// busyTypeName maps a PidLidBusyStatus value to its LegacyFreeBusyType string. The
// reference clamps an absent or out-of-range status to 0 (Free); this mirrors that.
func busyTypeName(status int32) string {
	switch status {
	case 1:
		return "Tentative"
	case 2:
		return "Busy"
	case 3:
		return "OOF"
	case 4:
		return "WorkingElsewhere"
	default:
		return "Free"
	}
}

// parseAvailabilityTime parses an EWS xs:dateTime. A value carrying an explicit
// offset (Z or ±hh:mm) is honoured; an offset-less local value is read as UTC,
// the documented v1 simplification (the request TimeZone bias is not applied).
func parseAvailabilityTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// namedTag combines a resolved named-property id with its type into a full
// proptag, or 0 when the name was never allocated in this mailbox.
func namedTag(id uint16, typ mapi.PropType) mapi.PropTag {
	if id == 0 {
		return 0
	}
	return mapi.PropTag(uint32(id)<<16 | uint32(typ))
}

// ntTimeVal reads a PtSysTime property as a UTC time (ok false when absent).
func ntTimeVal(pv mapi.PropertyValues, tag mapi.PropTag) (time.Time, bool) {
	if tag == 0 {
		return time.Time{}, false
	}
	if v, ok := pv.Get(tag); ok {
		if nt, ok := v.(uint64); ok {
			return mapi.NTTimeToUnix(nt).UTC(), true
		}
	}
	return time.Time{}, false
}

// longVal reads a PtLong property (0 when absent).
func longVal(pv mapi.PropertyValues, tag mapi.PropTag) int32 {
	if tag == 0 {
		return 0
	}
	if v, ok := pv.Get(tag); ok {
		if n, ok := v.(int32); ok {
			return n
		}
	}
	return 0
}

// boolVal reads a PtBoolean property (false when absent).
func boolVal(pv mapi.PropertyValues, tag mapi.PropTag) bool {
	if tag == 0 {
		return false
	}
	if v, ok := pv.Get(tag); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// strVal reads a PtUnicode property ("" when absent).
func strVal(pv mapi.PropertyValues, tag mapi.PropTag) string {
	if tag == 0 {
		return ""
	}
	if v, ok := pv.Get(tag); ok {
		if str, ok := v.(string); ok {
			return str
		}
	}
	return ""
}
