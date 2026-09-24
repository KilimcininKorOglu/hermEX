package dav

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/oxtask"
)

// defaultMaxICal caps a calendar PUT body; an event, even a recurring one preserved
// verbatim, is far smaller. It is the fallback when no operator limit is set.
const defaultMaxICal = 4 << 20

// icalOptions adapts the store's named-property allocator to oxcical.
func icalOptions(st *objectstore.Store) oxcical.Options {
	return oxcical.Options{Resolver: st.GetNamedPropIDs}
}

// handleCalGet serves a calendar object as an iCalendar text. HEAD returns the
// same headers with no body. It mirrors handleGet for the Calendar folder.
func (s *Server) handleCalGet(w http.ResponseWriter, r *http.Request, mailbox string) {
	st, fid, name, ok := s.openObjectCollection(w, r, mailbox, calTarget, false)
	if !ok {
		return
	}
	defer st.Close()

	obj, found, err := findObjectByName(st, fid, ".ics", name)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	msg, err := st.OpenMessage(obj.ID)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	ics, err := exportCalObject(st, fid, msg, name)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("ETag", etag(obj.ChangeNumber))
	w.Header().Set("Cache-Control", objectCacheControl)
	// A scheduling object (one carrying an ORGANIZER, stored with recipients) also
	// reports its CALDAV:schedule-tag (RFC 6638 8.2); a plain appointment does not.
	if eventsCollection(fid) && msgIsScheduling(msg) {
		w.Header().Set("Schedule-Tag", scheduleTag(obj.ChangeNumber))
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	// Final response body; a write failure means the client is gone, with no recourse.
	// #nosec G705 -- the daemon stamps X-Content-Type-Options: nosniff and the Content-Type is set explicitly, so the bytes are never interpreted as a document
	_, _ = w.Write(ics)
}

// exportCalObject serializes a stored object as the iCalendar its collection
// serves: VTODO from the shared task model in Tasks, VJOURNAL from the verbatim
// stored source in Journal, and VEVENT elsewhere.
func exportCalObject(st *objectstore.Store, fid int64, msg *oxcmail.Message, name string) ([]byte, error) {
	switch fid {
	case int64(mapi.PrivateFIDTasks):
		tk, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
		if err != nil {
			return nil, err
		}
		return oxcical.ExportVTODO(tk, name, time.Time{}), nil
	case int64(mapi.PrivateFIDJournal):
		return oxcical.ExportVJournal(msg, name), nil
	}
	return oxcical.Export(msg, icalOptions(st))
}

// handleCalPut creates or replaces a calendar object from an iCalendar body. It
// honors If-None-Match: * (create-only) and If-Match (replace-guard), responding
// 201 on create and 204 on replace with the new ETag. Mirrors handlePut.
func (s *Server) handleCalPut(w http.ResponseWriter, r *http.Request, user, mailbox string) {
	st, fid, name, ok := s.openObjectCollection(w, r, mailbox, calTarget, true)
	if !ok {
		return
	}
	defer st.Close()

	existing, found, err := findObjectByName(st, fid, ".ics", name)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	if failure, status := calPutPrecondition(r, existing, found); failure != "" {
		http.Error(w, failure, status)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, s.icalLimit()))
	if err != nil {
		s.davError(w, err, http.StatusBadRequest)
		return
	}
	msg, status, err := s.importCalendarBody(st, fid, user, body)
	if err != nil {
		s.davError(w, err, status)
		return
	}
	tag, _, err := resourceNameTag(st, true)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	msg.Props.Set(tag, name)

	// Capture the prior iCalendar before replacing it so implicit scheduling can diff
	// old against new to decide which attendees to (re-)invite or cancel (RFC 6638 §3).
	oldBody := priorCalendarBody(st, fid, existing, found)

	// Replace is delete-then-create: the object store has no in-place updater.
	if err := replaceObject(st, fid, msg, existing, found); err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}

	created, _, cerr := findObjectByName(st, fid, ".ics", name)
	if cerr == nil {
		s.stampCalPutTags(w, st, created, fid, body, user, oldBody)
	}

	if found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// priorCalendarBody returns the iCalendar an object held before a PUT replaced it,
// the "old" side of the implicit-scheduling diff. The Tasks (VTODO) and Journal
// (VJOURNAL) folders never schedule, so they report nothing.
func priorCalendarBody(st *objectstore.Store, fid int64, existing objectstore.FolderObject, found bool) string {
	if !found || !eventsCollection(fid) {
		return ""
	}
	body, err := calendarData(st, existing.ID)
	if err != nil {
		return ""
	}
	return body
}

// stampCalPutTags writes the ETag and, for a scheduling object, the schedule-tag
// (which changes on every direct PUT, RFC 6638 3.2.10 rule 3 / 8.2), then runs
// implicit scheduling (RFC 6638 3): the iTIP this change implies is auto-delivered.
// The diff is between the re-exported old and new forms, both normalized through
// the store, so a synthesized field (an absent DTEND filled from DTSTART, say)
// cannot read as a spurious change and re-invite everyone. Events-only and
// best-effort: the calendar write has committed, so a delivery failure is logged,
// never surfaced as a PUT error.
func (s *Server) stampCalPutTags(w http.ResponseWriter, st *objectstore.Store,
	created objectstore.FolderObject, fid int64, body []byte, user, oldBody string) {
	if created.ChangeNumber != 0 {
		w.Header().Set("ETag", etag(created.ChangeNumber))
		if eventsCollection(fid) && isSchedulingBody(string(body)) {
			w.Header().Set("Schedule-Tag", scheduleTag(created.ChangeNumber))
		}
	}
	if !eventsCollection(fid) {
		return
	}
	if newBody, err := calendarData(st, created.ID); err == nil {
		s.scheduleOnChange(user, oldBody, newBody, false)
	}
}

// calPutPrecondition evaluates the conditional headers a calendar PUT may carry.
// If-Schedule-Tag-Match is the scheduling-aware precondition (RFC 6638 8.3); when
// present it supersedes If-Match, so an inconsequential server scheduling change
// does not block the PUT. A non-empty message is the failure to report.
func calPutPrecondition(r *http.Request, existing objectstore.FolderObject, found bool) (string, int) {
	if r.Header.Get("If-None-Match") == "*" && found {
		return "already exists", http.StatusPreconditionFailed
	}
	if ism := r.Header.Get("If-Schedule-Tag-Match"); ism != "" {
		if !found || ism != scheduleTag(existing.ChangeNumber) {
			return "schedule-tag mismatch", http.StatusPreconditionFailed
		}
		return "", 0
	}
	if im := r.Header.Get("If-Match"); im != "" {
		if !found || im != etag(existing.ChangeNumber) {
			return "etag mismatch", http.StatusPreconditionFailed
		}
	}
	return "", 0
}

// importCalendarBody converts a PUT body into the stored message its collection
// calls for: a VTODO in Tasks, a VJOURNAL in Journal, and a VEVENT elsewhere. On
// failure it also reports the HTTP status to answer with.
func (s *Server) importCalendarBody(st *objectstore.Store, fid int64, user string, body []byte) (*oxcmail.Message, int, error) {
	switch fid {
	case int64(mapi.PrivateFIDTasks):
		task, _, ok := oxcical.ParseVTODO(body)
		if !ok {
			return nil, http.StatusBadRequest, errInvalidVTODO
		}
		props, err := oxtask.ToProps(task, st.GetNamedPropIDs)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		return &oxcmail.Message{Props: props}, 0, nil
	case int64(mapi.PrivateFIDJournal):
		msg, err := oxcical.ImportVJournal(body, icalOptions(st))
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		return msg, 0, nil
	}
	opt := icalOptions(st)
	var zones oxcical.ZoneLosses
	opt.OnUnresolvedZone = zones.Add
	// A client may PUT a floating time, which means its own wall clock; the
	// mailbox owner's zone is this server's reading of that.
	opt.DefaultZone = directory.UserZone(s.accounts, user)
	msg, err := oxcical.Import(body, opt)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	s.logZoneLosses(&zones)
	return msg, 0, nil
}

// errInvalidVTODO is the body a Tasks PUT is refused with when its VTODO does not
// parse.
var errInvalidVTODO = errors.New("invalid VTODO")

// logZoneLosses records a calendar PUT whose times could not be bound to a zone.
// The write is accepted (the client's own reading of those times is unchanged),
// but each one is stored as if it were UTC, so the event can sit hours away from
// the hour its author picked. The zone id is recorded because it is the one fact
// needed to extend the zone table; no calendar content is.
func (s *Server) logZoneLosses(z *oxcical.ZoneLosses) {
	if z.Times() == 0 {
		return
	}
	s.Logger.Emit(logging.Event{
		Level:     logging.LevelWarn,
		Subsystem: logging.DAV,
		Name:      "calendar.zone_unresolved",
		Fields:    logging.Fields{"zones": z.ZoneIDs(), "times": z.Times()},
	})
}

// handleCalDelete removes a calendar object, honoring If-Match. Mirrors handleDelete.
func (s *Server) handleCalDelete(w http.ResponseWriter, r *http.Request, user, mailbox string) {
	st, fid, name, ok := s.openObjectCollection(w, r, mailbox, calTarget, false)
	if !ok {
		return
	}
	defer st.Close()

	obj, found, err := findObjectByName(st, fid, ".ics", name)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if failure, status := calDeletePrecondition(r, obj); failure != "" {
		http.Error(w, failure, status)
		return
	}
	// Capture the iCalendar before deleting so implicit scheduling can cancel the
	// meeting for its attendees (organizer delete) or decline it (attendee delete),
	// per RFC 6638 §3. Events only; the Tasks and Journal PIM folders never schedule.
	var oldBody string
	if eventsCollection(fid) {
		if ob, oerr := calendarData(st, obj.ID); oerr == nil {
			oldBody = ob
		}
	}
	// Route to the Recoverable Items dumpster (not a hard purge): the object leaves
	// the live view but its bumped change number is a sync-collection tombstone.
	if err := st.SoftDeleteObject(obj.ID); err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	// Best-effort iTIP cancel/decline; a delivery failure never fails the delete. An
	// attendee delete with Schedule-Reply:F sends no reply (RFC 6638 8.1).
	if eventsCollection(fid) {
		s.scheduleOnChange(user, oldBody, "", scheduleReplyF(r))
	}
	w.WriteHeader(http.StatusNoContent)
}

// calDeletePrecondition evaluates the conditional headers a calendar DELETE may
// carry. If-Schedule-Tag-Match supersedes If-Match (RFC 6638 8.3). A non-empty
// message is the failure to report, at the given status.
func calDeletePrecondition(r *http.Request, obj objectstore.FolderObject) (string, int) {
	if ism := r.Header.Get("If-Schedule-Tag-Match"); ism != "" {
		if ism != scheduleTag(obj.ChangeNumber) {
			return "schedule-tag mismatch", http.StatusPreconditionFailed
		}
		return "", 0
	}
	if im := r.Header.Get("If-Match"); im != "" && im != etag(obj.ChangeNumber) {
		return "etag mismatch", http.StatusPreconditionFailed
	}
	return "", 0
}

// scheduleReplyF reports whether a request asks to suppress the scheduling reply via
// the Schedule-Reply: F header (RFC 6638 8.1); absent or T means send the reply.
func scheduleReplyF(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Schedule-Reply")), "F")
}

// isSchedulingBody reports whether an iCalendar body is a scheduling object resource:
// a VEVENT carrying an ORGANIZER (RFC 6638 3.1).
func isSchedulingBody(body string) bool {
	return nodeOrganizer(firstVEvent(body)) != ""
}

// msgIsScheduling reports whether a stored calendar message is a scheduling object:
// it has attendees (recipients) or an organizer (sent-representing) identity.
func msgIsScheduling(msg *oxcmail.Message) bool {
	if len(msg.Recipients) > 0 {
		return true
	}
	_, ok := msg.Props.Get(mapi.PrSentRepresentingSmtpAddress)
	return ok
}
