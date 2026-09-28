package activesync

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mapi"
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

// serverSends reports whether the server itself sends the response to the
// organizer ([MS-ASCMD] MeetingResponse). Through protocol 14.1 the client sends it
// with SendMail, so a server copy would give the organizer two answers; from 16.0 the
// server sends it only when the request carries a SendResponse element.
func serverSends(protocol string, req *wbxml.Node) bool {
	if !strings.HasPrefix(protocol, "16.") {
		return false
	}
	return req.Child(wbxml.MRSendResponse) != nil
}

// airSyncBodyHTML is the airsyncbase:Type of an HTML body ([MS-ASAIRS] Type).
const airSyncBodyHTML = "2"

// replyOf reads what the server sends the organizer. An empty SendResponse sends
// a response with no body; an airsyncbase:Body in it becomes the body, and a
// ProposedStartTime with its ProposedEndTime proposes a new time ([MS-ASCMD]
// SendResponse). ok is false for a proposal that is not a valid span.
func replyOf(protocol string, req *wbxml.Node) (reply meeting.Reply, ok bool) {
	if !serverSends(protocol, req) {
		return meeting.Reply{}, true
	}
	sr := req.Child(wbxml.MRSendResponse)
	reply = meeting.Reply{Send: true, Body: airSyncBody(sr)}
	if b := sr.Child(wbxml.ABBody); b != nil {
		reply.HTML = b.ChildText(wbxml.ABType) == airSyncBodyHTML
	}
	reply.Proposal, ok = proposalOf(sr)
	return reply, ok
}

// proposalOf reads the new time a SendResponse proposes, nil when it proposes
// none. Each of ProposedStartTime and ProposedEndTime requires the other, and the
// span must not end before it starts; ok is false otherwise.
func proposalOf(sr *wbxml.Node) (*meeting.Proposal, bool) {
	startText, endText := sr.ChildText(wbxml.MRProposedStartTime), sr.ChildText(wbxml.MRProposedEndTime)
	if startText == "" && endText == "" {
		return nil, true
	}
	start, okStart := parseEASCalTime(startText)
	end, okEnd := parseEASCalTime(endText)
	if !okStart || !okEnd || end.Before(start) {
		return nil, false
	}
	return &meeting.Proposal{Start: mapi.UnixToNTTime(start), End: mapi.UnixToNTTime(end)}, true
}

// handleMeetingResponse answers the MeetingResponse command (MS-ASCMD): the device
// accepts, tentatively accepts, or declines meeting requests it received. Each
// Request is recorded through the shared meeting workflow, filing the appointment
// and notifying the organizer, and answered with the resulting CalendarId.
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

// requestedItem finds the item a Request answers: RequestId in the folder its
// CollectionId names.
func requestedItem(st *objectstore.Store, req *wbxml.Node) (int64, bool) {
	folderID, err := strconv.ParseInt(req.ChildText(wbxml.MRFolderID), 10, 64)
	if err != nil {
		return 0, false
	}
	uid, err := strconv.ParseUint(req.ChildText(wbxml.MRRequestID), 10, 32)
	if err != nil {
		return 0, false
	}
	info, err := st.MessageByUID(folderID, uint32(uid))
	if err != nil {
		return 0, false
	}
	return info.ID, true
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
	messageID, ok := requestedItem(st, req)
	if !ok {
		return result(mrStatusInvalid, "")
	}
	reply, ok := replyOf(sess.protocol, req)
	if !ok {
		return result(mrStatusInvalid, "")
	}

	calendarID, err := meeting.RespondWith(st, s.accounts, s.Spool, sess.user, messageID, response, reply)
	if err != nil {
		return result(mrStatusError, "")
	}
	cid := ""
	if calendarID != 0 {
		cid = strconv.FormatInt(calendarID, 10)
	}
	return result(mrStatusOK, cid)
}
