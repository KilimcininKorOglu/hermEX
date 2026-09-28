package ews

import (
	"errors"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/meeting"
	"hermex/internal/oxews"
)

// meetingResponse is an AcceptItem/TentativelyAcceptItem/DeclineItem response
// object ([MS-OXWSMTGS]): it references the meeting request it answers and may
// carry the note the attendee wrote for the organizer.
type meetingResponse struct {
	ReferenceItemID refID `xml:"ReferenceItemId"`
	Body            struct {
		Type    string `xml:"BodyType,attr"`
		Content string `xml:",chardata"`
	} `xml:"Body"`
}

// reply is what the organizer receives with the response: the attendee's note, in
// the body type the client wrote it in. send, a SendOnly/SendAndSaveCopy
// disposition, asks for the organizer to be notified at all.
func (mr meetingResponse) reply(send bool) meeting.Reply {
	return meeting.Reply{
		Send: send,
		Body: mr.Body.Content,
		HTML: strings.EqualFold(mr.Body.Type, "HTML"),
	}
}

// meetingRespond records an attendee's response to the referenced meeting request
// through the shared meeting workflow (stamp, file the appointment, notify the
// organizer) and reports success.
func (s *Server) meetingRespond(sess *session, mr meetingResponse, response int32, send bool) itemResponseMessage {
	id, err := oxews.DecodeItemID(mr.ReferenceItemID.ID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	// The request id self-encodes its mailbox; responding to a delegated meeting is
	// gated on edit access to its folder. The responder is the mailbox owner
	// (respond-on-behalf, the organizer is notified as the principal), so for the
	// caller's own mailbox that is the caller, and for a delegated one it is the target.
	cache := s.newStoreCache()
	defer cache.closeAll()
	st, code := cache.openForItem(sess, id, mapi.FrightsEditAny)
	if code != "" {
		return itemError(code)
	}
	responder, actor, code := s.meetingResponder(sess, id, send)
	if code != "" {
		return itemError(code)
	}
	if _, err := meeting.RespondOnBehalfWith(st, s.accounts, s.Spool, responder, actor, id.MessageID, response, mr.reply(send)); err != nil {
		if errors.Is(err, meeting.ErrRequestNotFound) {
			return itemError("ErrorItemNotFound")
		}
		return itemError("ErrorInternalServerError")
	}
	return meetingResponseOK()
}

// meetingResponder names who a meeting response is from and who submits it. The
// attendee is the mailbox the request is in: the caller's own, or the delegated one
// the item id names. A response that notifies the organizer goes out from that
// mailbox, so in a delegated one the caller answers only under a send-as or
// send-on-behalf grant, the decision every send path shares, and an on-behalf
// answer names the caller as its sender. A response that only records the answer
// sends nothing and needs no grant.
func (s *Server) meetingResponder(sess *session, id oxews.ItemID, send bool) (attendee, actor, code string) {
	attendee = sess.user
	if id.Mailbox != "" {
		attendee = id.Mailbox
	}
	if !send {
		return attendee, attendee, ""
	}
	attendee, actor, ok := s.resolveSender(sess.user, attendee)
	if !ok {
		return "", "", "ErrorSendAsDenied"
	}
	return attendee, actor, ""
}

// meetingResponseOK is a success response message for one meeting response. The
// empty Items container is present because clients reject a CreateItemResponseMessage
// without one.
func meetingResponseOK() itemResponseMessage {
	return itemResponseMessage{ResponseClass: "Success", ResponseCode: "NoError", Items: &itemsWrap{}}
}
