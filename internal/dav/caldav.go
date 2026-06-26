package dav

import (
	"io"
	"net/http"

	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
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
	ics, err := oxcical.Export(msg, icalOptions(st))
	if err != nil {
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
func (s *Server) handleCalPut(w http.ResponseWriter, r *http.Request, mailbox string) {
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
	msg, err := oxcical.Import(body, icalOptions(st))
	if err != nil {
		http.Error(w, "invalid iCalendar: "+err.Error(), http.StatusBadRequest)
		return
	}
	tag, _, err := resourceNameTag(st, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	msg.Props.Set(tag, name)

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

	created, _, err := findObjectByName(st, fid, ".ics", name)
	if err == nil && created.ChangeNumber != 0 {
		w.Header().Set("ETag", etag(created.ChangeNumber))
	}
	if found {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

// handleCalDelete removes a calendar object, honoring If-Match. Mirrors handleDelete.
func (s *Server) handleCalDelete(w http.ResponseWriter, r *http.Request, mailbox string) {
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
	// Route to the Recoverable Items dumpster (not a hard purge): the object leaves
	// the live view but its bumped change number is a sync-collection tombstone.
	if err := st.SoftDeleteObject(obj.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
