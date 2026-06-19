package activesync

import (
	"net/http"
	"strconv"

	"hermex/internal/meeting"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// MeetingResponse Status codes (MS-ASCMD 2.2.3.144.4).
const (
	mrStatusOK      = 1 // the response was processed
	mrStatusInvalid = 2 // an invalid meeting request
	mrStatusError   = 3 // the server failed to process the response
)

// userResponse maps an EAS UserResponse (1 accept, 2 tentative, 3 decline) to the
// shared meeting response code.
func userResponse(u int) (int32, bool) {
	switch u {
	case 1:
		return meeting.ResponseAccepted, true
	case 2:
		return meeting.ResponseTentative, true
	case 3:
		return meeting.ResponseDeclined, true
	}
	return 0, false
}

// handleMeetingResponse answers the MeetingResponse command (MS-ASCMD): the device
// accepts, tentatively accepts, or declines meeting requests it received. Each
// Request is recorded through the shared meeting workflow — filing the appointment
// and notifying the organizer — and answered with the resulting CalendarId.
func (s *Server) handleMeetingResponse(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "malformed WBXML", http.StatusBadRequest)
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()

	var results []*wbxml.Node
	for _, req := range root.Children {
		if req.Tag == wbxml.MRRequest {
			results = append(results, s.respondMeeting(st, sess, req))
		}
	}
	writeWBXML(w, wbxml.Elem(wbxml.MRMeetingResponse, results...))
}

// respondMeeting processes one MeetingResponse Request and builds its Result.
func (s *Server) respondMeeting(st *objectstore.Store, sess *session, req *wbxml.Node) *wbxml.Node {
	requestID := req.ChildText(wbxml.MRRequestID)
	result := func(status int, calendarID string) *wbxml.Node {
		n := wbxml.Elem(wbxml.MRResult,
			wbxml.Str(wbxml.MRRequestID, requestID),
			wbxml.Str(wbxml.MRStatus, strconv.Itoa(status)))
		if calendarID != "" {
			n.Children = append(n.Children, wbxml.Str(wbxml.MRCalendarID, calendarID))
		}
		return n
	}

	ur, _ := strconv.Atoi(req.ChildText(wbxml.MRUserResponse))
	response, ok := userResponse(ur)
	if !ok {
		return result(mrStatusInvalid, "")
	}
	folderID, err := strconv.ParseInt(req.ChildText(wbxml.MRFolderID), 10, 64)
	if err != nil {
		return result(mrStatusInvalid, "")
	}
	uid64, err := strconv.ParseUint(requestID, 10, 32)
	if err != nil {
		return result(mrStatusInvalid, "")
	}
	info, err := st.MessageByUID(folderID, uint32(uid64))
	if err != nil {
		return result(mrStatusInvalid, "")
	}

	// AS 14.0+ has the server send the response to the organizer.
	calendarID, err := meeting.Respond(st, s.accounts, s.Spool, sess.user, info.ID, response, true)
	if err != nil {
		return result(mrStatusError, "")
	}
	cid := ""
	if calendarID != 0 {
		cid = strconv.FormatInt(calendarID, 10)
	}
	return result(mrStatusOK, cid)
}
