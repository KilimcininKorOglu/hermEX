package ews

import (
	"errors"

	"hermex/internal/mapi"
	"hermex/internal/meeting"
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
func (s *Server) meetingRespond(sess *session, ref refID, response int32, send bool) itemResponseMessage {
	id, err := oxews.DecodeItemID(ref.ID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	// The request id self-encodes its mailbox; responding to a delegated meeting is
	// gated on edit access to its folder. The responder is the mailbox owner
	// (respond-on-behalf — the organizer is notified as the principal), so for the
	// caller's own mailbox that is the caller, and for a delegated one it is the target.
	cache := s.newStoreCache()
	defer cache.closeAll()
	st, code := cache.openForItem(sess, id, mapi.FrightsEditAny)
	if code != "" {
		return itemError(code)
	}
	responder := sess.user
	if id.Mailbox != "" {
		responder = id.Mailbox
	}
	if _, err := meeting.Respond(st, s.accounts, s.Spool, responder, id.MessageID, response, send); err != nil {
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
