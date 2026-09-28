package webmail2api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/meeting"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// findCalendarPart returns the decoded iCalendar (text/calendar or an .ics
// attachment) carried by a message, or nil when it holds no invite.
func findCalendarPart(root *mime.Part) []byte {
	var found []byte
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if p == nil || found != nil {
			return
		}
		isCal := (p.Type == "text" && p.Subtype == "calendar") ||
			(p.Type == "application" && p.Subtype == "ics")
		if isCal {
			if c, err := p.DecodedContent(); err == nil {
				found = c
				return
			}
		}
		for _, ch := range p.Children {
			walk(ch)
		}
	}
	walk(root)
	return found
}

// organizerAddress extracts the SMTP address from an ORGANIZER value, which is
// usually "mailto:user@host" but may be a bare address.
func organizerAddress(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.LastIndex(strings.ToLower(v), "mailto:"); i >= 0 {
		return strings.TrimSpace(v[i+len("mailto:"):])
	}
	return v
}

// inviteJSON is what the reader shows about a message carrying a calendar part:
// the meeting it describes, and, for a meeting message, its kind and the state that
// goes with it. Kind is empty for a calendar part that is not a meeting message,
// such as an event shared as an attachment. Response is the answer the mailbox
// gave to a request, or the answer a response carries, in the words /mail/rsvp
// takes.
type inviteJSON struct {
	IsInvite          bool   `json:"isInvite"`
	Kind              string `json:"kind,omitempty"`
	UID               string `json:"uid,omitempty"`
	Summary           string `json:"summary,omitempty"`
	Start             string `json:"start,omitempty"`
	End               string `json:"end,omitempty"`
	Location          string `json:"location,omitempty"`
	Organizer         string `json:"organizer,omitempty"`
	Response          string `json:"response,omitempty"`
	ResponseRequested bool   `json:"responseRequested,omitempty"`
	IsOrganizer       bool   `json:"isOrganizer,omitempty"`
	ProposedStart     string `json:"proposedStart,omitempty"`
	ProposedEnd       string `json:"proposedEnd,omitempty"`
	Removable         bool   `json:"removable,omitempty"`
}

// handleInvite reports whether a message carries a meeting and, when it does, the
// details parsed from its embedded iCalendar and what the meeting workflow knows
// about it: a request the reader can answer, the answer already given, an
// attendee's answer or proposed time, or a cancellation whose meeting is still on
// the calendar.
func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"), accessRead)
	if !ok {
		return
	}
	defer st.Close()
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		writeJSON(w, http.StatusOK, inviteJSON{})
		return
	}
	ics := findCalendarPart(mime.ParseStructure(raw))
	if ics == nil {
		writeJSON(w, http.StatusOK, inviteJSON{})
		return
	}
	out := inviteDetails(ics)
	if err := describeMeeting(st, fid, uid, ics, &out); err != nil {
		logError("invite", err, logging.Fields{})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the meeting"})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// inviteDetails reads the meeting a calendar part describes.
func inviteDetails(ics []byte) inviteJSON {
	e := icalToEvent(ics, 0)
	organizer, _ := icalProp(ics, "ORGANIZER")
	return inviteJSON{IsInvite: true, UID: e.UID, Summary: e.Summary, Start: e.Start, End: e.End,
		Location: e.Location, Organizer: organizerAddress(organizer)}
}

// describeMeeting adds to out what the meeting workflow knows about the meeting
// message at (fid, uid). A message that is not a meeting message adds nothing.
func describeMeeting(st *objectstore.Store, fid int64, uid uint32, ics []byte, out *inviteJSON) error {
	info, err := st.MessageByUID(fid, uid)
	if err != nil {
		return err
	}
	v, ok, err := meeting.Describe(st, info.ID, ics)
	if err != nil || !ok {
		return err
	}
	out.Kind = string(v.Kind)
	out.Response = meetingResponseWord(v.Response)
	out.ResponseRequested = v.ResponseRequested
	out.IsOrganizer = v.Organizer
	out.Removable = v.Removable
	if v.Proposal != nil {
		out.ProposedStart = mapi.NTTimeToUnix(v.Proposal.Start).UTC().Format(time.RFC3339)
		out.ProposedEnd = mapi.NTTimeToUnix(v.Proposal.End).UTC().Format(time.RFC3339)
	}
	return nil
}

// handleExportICS streams a message's embedded meeting invite as an .ics file.
// The calendar part carries the original iTIP METHOD, VEVENT, and any VTIMEZONE,
// so it is served verbatim rather than round-tripped through oxcical (which would
// drop the METHOD and other iTIP-only fields). Messages without a calendar part
// are not invites and return 404.
func (s *Server) handleExportICS(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"), accessRead)
	if !ok {
		return
	}
	defer st.Close()
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	ics := findCalendarPart(mime.ParseStructure(raw))
	if ics == nil {
		http.Error(w, "no calendar invite in message", http.StatusNotFound)
		return
	}
	name := icsFilename(ics)
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	_, _ = w.Write(ics)
}

// icsFilename derives a safe download name from the invite's SUMMARY (falling
// back to its UID, then a constant), always ending in ".ics".
func icsFilename(ics []byte) string {
	base, _ := icalProp(ics, "SUMMARY")
	if strings.TrimSpace(base) == "" {
		base, _ = icalProp(ics, "UID")
	}
	base = sanitizeICSName(base)
	if base == "" {
		base = "invite"
	}
	return base + ".ics"
}

// sanitizeICSName keeps a filename safe for a Content-Disposition header: it
// drops path separators, quotes, and control characters, collapses whitespace to
// underscores, and bounds the length.
func sanitizeICSName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if repl, keep := icsNameRune(r); keep {
			b.WriteRune(repl)
		}
		if b.Len() >= 80 {
			break
		}
	}
	return strings.Trim(b.String(), "_.")
}

// icsNameRune maps one rune to what the filename may carry: whitespace becomes
// an underscore, control characters and the path/quoting characters are dropped,
// everything else passes through.
func icsNameRune(r rune) (rune, bool) {
	switch {
	case r < 0x20 || r == 0x7f:
		return 0, false
	case r == '/' || r == '\\' || r == '"' || r == ':':
		return 0, false
	case r == ' ' || r == '\t':
		return '_', true
	}
	return r, true
}

// meetingResponseCode maps the SPA's response vocabulary to the stored response
// value. An unknown word yields 0, which the caller refuses.
func meetingResponseCode(response string) int32 {
	switch response {
	case "accept":
		return meeting.ResponseAccepted
	case "tentative":
		return meeting.ResponseTentative
	case "decline":
		return meeting.ResponseDeclined
	}
	return 0
}

// meetingResponseWord is meetingResponseCode the other way round: the SPA's word
// for a stored response, "" for none.
func meetingResponseWord(response int32) string {
	switch response {
	case meeting.ResponseAccepted:
		return "accept"
	case meeting.ResponseTentative:
		return "tentative"
	case meeting.ResponseDeclined:
		return "decline"
	}
	return ""
}

// handleRemoveFromCalendar takes off the calendar the meeting, or the one instance
// of a series, the cancellation the reader opened calls off: the attendee removing
// a meeting its organizer canceled ([MS-OXOCAL] 3.1.4.9.2). Only what the calendar
// holds as canceled is removed, so the cancellation of a meeting that has since
// been renewed, or one its organizer did not send, removes nothing.
func (s *Server) handleRemoveFromCalendar(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	mb, fid, uid, ok := s.locateMailbox(w, r, req.ID, accessRead)
	if !ok {
		return
	}
	defer mb.st.Close()
	if !mb.writeAllowed(int64(mapi.PrivateFIDCalendar)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	id, ics, ok := cancellationOf(mb.st, fid, uid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	err := meeting.RemoveCanceled(mb.st, id, ics)
	switch {
	case errors.Is(err, meeting.ErrNotCanceled):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "nothing canceled is on the calendar"})
	case err != nil:
		logError("remove-canceled", err, logging.Fields{"user": mb.user})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not remove the meeting"})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
	}
}

// cancellationOf reads the message at (fid, uid) and the calendar part it carries.
func cancellationOf(st *objectstore.Store, fid int64, uid uint32) (int64, []byte, bool) {
	info, err := st.MessageByUID(fid, uid)
	if err != nil {
		return 0, nil, false
	}
	raw, err := st.GetMessageRaw(fid, uid)
	if err != nil {
		return 0, nil, false
	}
	ics := findCalendarPart(mime.ParseStructure(raw))
	return info.ID, ics, ics != nil
}

// rsvpRequest is an answer to a meeting request, with the choices Outlook offers
// beside it: whether the organizer is sent the answer at all, the note that goes
// with it, and a new time it proposes ([MS-OXOCAL] 3.1.4.8.4.1). An absent send
// sends, the way every answer did before the choice existed.
type rsvpRequest struct {
	ID           string `json:"id"`
	Response     string `json:"response"`
	Send         *bool  `json:"send"`
	Comment      string `json:"comment"`
	ProposeStart string `json:"proposeStart"`
	ProposeEnd   string `json:"proposeEnd"`
}

// reply reads what the organizer receives. ok is false for a proposed time that is
// not a span, ends before it starts, or would not be sent: a proposal nobody
// receives proposes nothing.
func (req rsvpRequest) reply() (meeting.Reply, bool) {
	reply := meeting.Reply{Send: req.Send == nil || *req.Send, Body: req.Comment}
	if req.ProposeStart == "" && req.ProposeEnd == "" {
		return reply, true
	}
	start, err1 := time.Parse(time.RFC3339, req.ProposeStart)
	end, err2 := time.Parse(time.RFC3339, req.ProposeEnd)
	if errors.Join(err1, err2) != nil || end.Before(start) || !reply.Send {
		return reply, false
	}
	reply.Proposal = &meeting.Proposal{Start: mapi.UnixToNTTime(start), End: mapi.UnixToNTTime(end)}
	return reply, true
}

// rsvpStatus is the status an answer reports, by response.
var rsvpStatus = map[int32]string{
	meeting.ResponseAccepted:  "accepted",
	meeting.ResponseTentative: "tentative",
	meeting.ResponseDeclined:  "declined",
}

// handleRSVP responds to a meeting invite through the SAME model every other
// protocol answers with: the answer is recorded and the calendar updated, and,
// unless the reader chose not to send it, the organizer is sent the answer with the
// reader's note and any new time proposed, and the reader keeps it in Sent Items.
//
// It used to answer on its own: declining recorded nothing at all, the response
// properties the organizer's tracking reads were never written, and accepting
// filed a fresh appointment with no regard for one already there, so answering
// twice, or answering after the server had auto-processed the invitation, left
// two appointments for one meeting.
func (s *Server) handleRSVP(w http.ResponseWriter, r *http.Request) {
	var req rsvpRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	response := meetingResponseCode(req.Response)
	if response == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "response must be accept, tentative or decline"})
		return
	}
	reply, ok := req.reply()
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a proposed time is a start and an end, and it is sent"})
		return
	}
	mb, fid, uid, ok := s.locateMailbox(w, r, req.ID, accessWrite)
	if !ok {
		return
	}
	defer mb.st.Close()
	attendee, actor, allowed := s.rsvpIdentity(mb, reply.Send)
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	info, err := mb.st.MessageByUID(fid, uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	// This is the reader's own answer, so it also clears the request mail when the
	// mailbox asked for that. The response sent to the organizer is kept in the
	// caller's own Sent Items, like everything else sent from here.
	c, _ := s.session(r)
	reply.SentCopy = func(raw []byte) { s.fileCallerSentCopy(mb, c, raw, "meeting-response") }
	if _, err := meeting.RespondOnBehalfWith(mb.st, s.accounts, s.spool, attendee, actor, info.ID, response, reply); err != nil {
		rsvpFailure(w, err, mb.user)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": rsvpStatus[response]})
}

// rsvpIdentity names who an answer is from and who submits it. In a shared mailbox
// the attendee is the mailbox, not the delegate answering for it. An answer the
// organizer is sent goes out from that mailbox, so the delegate sends it only under
// a send-as or send-on-behalf grant, the decision every send path shares, and an
// on-behalf answer names the delegate as its Sender. An answer that is only
// recorded sends nothing and needs no grant.
func (s *Server) rsvpIdentity(mb *mailboxCtx, send bool) (attendee, actor string, ok bool) {
	if !send {
		return mb.identity(), mb.identity(), true
	}
	return s.resolveSender(mb.user, mb.identity())
}

// rsvpFailure answers a response the meeting workflow refused: a message that is
// gone, one that is not an invitation (a meeting response, a counter proposal or a
// cancellation carries a calendar part too), or a meeting the mailbox organizes or
// its organizer canceled. Anything else is a failure to record, logged here.
func rsvpFailure(w http.ResponseWriter, err error, user string) {
	switch {
	case errors.Is(err, meeting.ErrRequestNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, meeting.ErrNotARequest):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a meeting request"})
	case errors.Is(err, meeting.ErrOrganizer):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "you organize this meeting"})
	case errors.Is(err, meeting.ErrCanceled):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the meeting was canceled"})
	default:
		logError("rsvp", err, logging.Fields{"user": user})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record the response"})
	}
}
