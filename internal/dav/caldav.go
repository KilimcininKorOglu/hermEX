package dav

import (
	"io"
	"net/http"
	"time"

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
	kind, _, coll, name := classify(r.URL.Path)
	if kind != kindCalObject {
		http.Error(w, "not a calendar resource", http.StatusMethodNotAllowed)
		return
	}
	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	fid, ok, err := calCollectionFID(st, coll)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such calendar", http.StatusNotFound)
		return
	}
	obj, found, err := findObjectByName(st, fid, ".ics", name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	msg, err := st.OpenMessage(obj.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var ics []byte
	if fid == int64(mapi.PrivateFIDTasks) {
		// The Tasks collection serves VTODO from the shared task model.
		tk, terr := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
		if terr != nil {
			http.Error(w, terr.Error(), http.StatusInternalServerError)
			return
		}
		ics = oxcical.ExportVTODO(tk, name, time.Time{})
	} else if ics, err = oxcical.Export(msg, icalOptions(st)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("ETag", etag(obj.ChangeNumber))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Write(ics)
}

// handleCalPut creates or replaces a calendar object from an iCalendar body. It
// honors If-None-Match: * (create-only) and If-Match (replace-guard), responding
// 201 on create and 204 on replace with the new ETag. Mirrors handlePut.
func (s *Server) handleCalPut(w http.ResponseWriter, r *http.Request, user, mailbox string) {
	kind, _, coll, name := classify(r.URL.Path)
	if kind != kindCalObject {
		http.Error(w, "not a calendar resource", http.StatusMethodNotAllowed)
		return
	}
	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	fid, ok, err := calCollectionFID(st, coll)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such calendar", http.StatusNotFound)
		return
	}
	existing, found, err := findObjectByName(st, fid, ".ics", name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("If-None-Match") == "*" && found {
		http.Error(w, "already exists", http.StatusPreconditionFailed)
		return
	}
	if im := r.Header.Get("If-Match"); im != "" {
		if !found || im != etag(existing.ChangeNumber) {
			http.Error(w, "etag mismatch", http.StatusPreconditionFailed)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, s.icalLimit()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var msg *oxcmail.Message
	if fid == int64(mapi.PrivateFIDTasks) {
		task, _, ok := oxcical.ParseVTODO(body)
		if !ok {
			http.Error(w, "invalid VTODO", http.StatusBadRequest)
			return
		}
		props, perr := oxtask.ToProps(task, st.GetNamedPropIDs)
		if perr != nil {
			http.Error(w, perr.Error(), http.StatusInternalServerError)
			return
		}
		msg = &oxcmail.Message{Props: props}
	} else if msg, err = oxcical.Import(body, icalOptions(st)); err != nil {
		http.Error(w, "invalid iCalendar: "+err.Error(), http.StatusBadRequest)
		return
	}
	tag, _, err := resourceNameTag(st, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	msg.Props.Set(tag, name)

	// Capture the prior iCalendar before replacing it so implicit scheduling can diff
	// old against new to decide which attendees to (re-)invite or cancel (RFC 6638
	// §3). Tasks (VTODO) never schedule, so the diff is skipped for them.
	var oldBody string
	if found && fid != int64(mapi.PrivateFIDTasks) {
		if ob, oerr := calendarData(st, existing.ID); oerr == nil {
			oldBody = ob
		}
	}

	// Replace is delete-then-create: the object store has no in-place updater.
	if found {
		if err := st.DeleteObject(existing.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if _, err := st.CreateMessage(fid, msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	created, _, cerr := findObjectByName(st, fid, ".ics", name)
	if cerr == nil && created.ChangeNumber != 0 {
		w.Header().Set("ETag", etag(created.ChangeNumber))
	}

	// Implicit scheduling (RFC 6638 §3): auto-deliver the iTIP this change implies.
	// The diff is between the re-exported old and new forms, both normalized through
	// the store, so a synthesized field (e.g. an absent DTEND filled from DTSTART)
	// cannot read as a spurious change and re-invite everyone. Events-only and
	// best-effort: the calendar write has committed, so a delivery failure is logged,
	// never surfaced as a PUT error.
	if fid != int64(mapi.PrivateFIDTasks) && cerr == nil {
		if newBody, nerr := calendarData(st, created.ID); nerr == nil {
			s.scheduleOnChange(user, oldBody, newBody)
		}
	}

	if found {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

// handleCalDelete removes a calendar object, honoring If-Match. Mirrors handleDelete.
func (s *Server) handleCalDelete(w http.ResponseWriter, r *http.Request, user, mailbox string) {
	kind, _, coll, name := classify(r.URL.Path)
	if kind != kindCalObject {
		http.Error(w, "not a calendar resource", http.StatusMethodNotAllowed)
		return
	}
	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	fid, ok, err := calCollectionFID(st, coll)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such calendar", http.StatusNotFound)
		return
	}
	obj, found, err := findObjectByName(st, fid, ".ics", name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if im := r.Header.Get("If-Match"); im != "" && im != etag(obj.ChangeNumber) {
		http.Error(w, "etag mismatch", http.StatusPreconditionFailed)
		return
	}
	// Capture the iCalendar before deleting so implicit scheduling can cancel the
	// meeting for its attendees (organizer delete) or decline it (attendee delete),
	// per RFC 6638 §3. Events only; Tasks (VTODO) never schedule.
	var oldBody string
	if fid != int64(mapi.PrivateFIDTasks) {
		if ob, oerr := calendarData(st, obj.ID); oerr == nil {
			oldBody = ob
		}
	}
	// Route to the Recoverable Items dumpster (not a hard purge): the object leaves
	// the live view but its bumped change number is a sync-collection tombstone.
	if err := st.SoftDeleteObject(obj.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Best-effort iTIP cancel/decline; a delivery failure never fails the delete.
	if fid != int64(mapi.PrivateFIDTasks) {
		s.scheduleOnChange(user, oldBody, "")
	}
	w.WriteHeader(http.StatusNoContent)
}
