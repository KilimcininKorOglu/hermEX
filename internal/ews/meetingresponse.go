package ews

import (
	"errors"

	"hermex/internal/meeting"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// meetingResponse is an AcceptItem/TentativelyAcceptItem/DeclineItem response
// object ([MS-OXWSMTGS]): it references the meeting request it answers.
type meetingResponse struct {
	ReferenceItemID refID `xml:"ReferenceItemId"`
}

// meetingRespond records an attendee's response to the referenced meeting request
// through the shared meeting workflow (stamp, file the appointment, notify the
// organizer) and reports success. send — a SendOnly/SendAndSaveCopy disposition —
// asks for the organizer to be notified.
func (s *Server) meetingRespond(st *objectstore.Store, sess *session, ref refID, response int32, send bool) itemResponseMessage {
	id, err := oxews.DecodeItemID(ref.ID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	if id.Mailbox != "" {
		// Responding to another mailbox's meeting on its behalf is not yet supported.
		return itemError("ErrorAccessDenied")
	}
	if _, err := meeting.Respond(st, s.accounts, s.Spool, sess.user, id.MessageID, response, send); err != nil {
		if errors.Is(err, meeting.ErrRequestNotFound) {
			return itemError("ErrorItemNotFound")
		}
		return itemError("ErrorInternalServerError")
	}
	return meetingResponseOK()
}

// meetingResponseOK is a success response message for one meeting response. The
// empty Items container is present because clients reject a CreateItemResponseMessage
// without one.
func meetingResponseOK() itemResponseMessage {
	return itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", Items: &itemsWrap{}}
}
