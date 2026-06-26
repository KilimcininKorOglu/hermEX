package webmail2api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxtask"
)

// taskJSON / noteJSON are the SPA's Task and Note shapes. A task maps to the
// canonical oxtask named properties (the one model ActiveSync, EWS, and a MAPI
// client share); a note maps to PR_SUBJECT (title) and PR_BODY (body).
type taskJSON struct {
	UID         string `json:"uid"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	Due         string `json:"due,omitempty"`
	Completed   bool   `json:"completed"`
}

type noteJSON struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// propString returns a message property as a string (props may hold string or
// []byte for text values).
func propString(msg *oxcmail.Message, tag mapi.PropTag) string {
	v, ok := msg.Props.Get(tag)
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}

// jsonToTask / taskToJSON convert between the SPA's task shape and the canonical
// oxtask model. The SPA surfaces a subset (summary/description/due/completed); the
// other oxtask fields (reminder, importance, categories) set by ActiveSync or EWS on
// the same object are preserved on update by merging onto the stored task.
func jsonToTask(in taskJSON) oxtask.Task {
	t := oxtask.New()
	t.Subject = in.Summary
	t.Body = in.Description
	t.Complete = in.Completed
	if in.Due != "" {
		if due, ok := parseDue(in.Due); ok {
			t.Due = due
		}
	}
	return t
}

func taskToJSON(t oxtask.Task) taskJSON {
	j := taskJSON{Summary: t.Subject, Description: t.Body, Completed: t.Complete}
	if !t.Due.IsZero() {
		j.Due = formatDue(t.Due)
	}
	return j
}

// parseDue accepts a date-only (YYYY-MM-DD) or an RFC3339 due string.
func parseDue(s string) (time.Time, bool) {
	if len(s) == 10 {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t.UTC(), true
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// formatDue renders a due time as a date when it has no time-of-day, else RFC3339.
func formatDue(t time.Time) string {
	t = t.UTC()
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format(time.RFC3339)
}

// storeJSONItem stores a JSON payload as a message (class + subject for display,
// body = the JSON), returning the new object id. Used for the contact distribution
// list, whose member array has no scalar property model.
func storeJSONItem(st *objectstore.Store, folderID int64, class, subject string, payload any) (int64, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, class)
	props.Set(mapi.PrSubject, subject)
	props.Set(mapi.PrBody, string(b))
	return st.CreateMessage(folderID, &oxcmail.Message{Props: props})
}

// storeTask writes a task as the canonical named properties, returning the new id.
func (s *Server) storeTask(st *objectstore.Store, t oxtask.Task) (int64, error) {
	props, err := oxtask.ToProps(t, st.GetNamedPropIDs)
	if err != nil {
		return 0, err
	}
	return st.CreateMessage(mapi.PrivateFIDTasks, &oxcmail.Message{Props: props})
}

// ---- Tasks ----

func (s *Server) handleGetTasks(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	objs, _ := st.ListFolderObjects(mapi.PrivateFIDTasks)
	tasks := make([]taskJSON, 0, len(objs))
	for _, o := range objs {
		msg, err := st.OpenMessage(o.ID)
		if err != nil {
			continue
		}
		t, _ := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
		j := taskToJSON(t)
		j.UID = strconv.FormatInt(o.ID, 10)
		tasks = append(tasks, j)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var in taskJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	id, err := s.storeTask(st, jsonToTask(in))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save task"})
		return
	}
	in.UID = strconv.FormatInt(id, 10)
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	var in taskJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	// Merge the SPA's fields onto the stored task so fields it does not surface
	// (reminder, importance, categories) set by another protocol are not lost.
	merged := jsonToTask(in)
	if old, err := strconv.ParseInt(r.PathValue("uid"), 10, 64); err == nil {
		if msg, err := st.OpenMessage(old); err == nil {
			if prev, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs); err == nil {
				prev.Subject, prev.Body, prev.Complete, prev.Due = merged.Subject, merged.Body, merged.Complete, merged.Due
				merged = prev
			}
		}
		_ = st.DeleteObject(old)
	}
	id, err := s.storeTask(st, merged)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save task"})
		return
	}
	in.UID = strconv.FormatInt(id, 10)
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	s.deleteObjectByPath(w, r, "uid")
}

// ---- Notes ----

func (s *Server) handleGetNotes(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	objs, _ := st.ListFolderObjects(mapi.PrivateFIDNotes)
	notes := make([]noteJSON, 0, len(objs))
	for _, o := range objs {
		msg, err := st.OpenMessage(o.ID)
		if err != nil {
			continue
		}
		notes = append(notes, noteJSON{
			ID:    strconv.FormatInt(o.ID, 10),
			Title: propString(msg, mapi.PrSubject),
			Body:  propString(msg, mapi.PrBody),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes})
}

func (s *Server) handleCreateNote(w http.ResponseWriter, r *http.Request) {
	var in noteJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.StickyNote")
	props.Set(mapi.PrSubject, in.Title)
	props.Set(mapi.PrBody, in.Body)
	id, err := st.CreateMessage(mapi.PrivateFIDNotes, &oxcmail.Message{Props: props})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save note"})
		return
	}
	in.ID = strconv.FormatInt(id, 10)
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleUpdateNote(w http.ResponseWriter, r *http.Request) {
	var in noteJSON
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	if old, err := strconv.ParseInt(r.PathValue("id"), 10, 64); err == nil {
		_ = st.DeleteObject(old)
	}
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.StickyNote")
	props.Set(mapi.PrSubject, in.Title)
	props.Set(mapi.PrBody, in.Body)
	id, err := st.CreateMessage(mapi.PrivateFIDNotes, &oxcmail.Message{Props: props})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save note"})
		return
	}
	in.ID = strconv.FormatInt(id, 10)
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleDeleteNote(w http.ResponseWriter, r *http.Request) {
	s.deleteObjectByPath(w, r, "id")
}

// deleteObjectByPath deletes an object-store item whose id is in the named path
// segment.
func (s *Server) deleteObjectByPath(w http.ResponseWriter, r *http.Request, seg string) {
	id, err := strconv.ParseInt(r.PathValue(seg), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	if err := st.DeleteObject(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not delete"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
