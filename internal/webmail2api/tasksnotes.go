package webmail2api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// taskJSON / noteJSON are the SPA's Task and Note shapes. They have no MS-OX
// converter (no external VTODO/note client consumes them), so they round-trip
// as a JSON payload in the message body, with the title in PR_SUBJECT.
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

// storeJSONItem stores a JSON payload as a message (class + subject for display,
// body = the JSON), returning the new object id.
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
		var t taskJSON
		_ = json.Unmarshal([]byte(propString(msg, mapi.PrBody)), &t)
		t.UID = strconv.FormatInt(o.ID, 10)
		tasks = append(tasks, t)
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
	id, err := storeJSONItem(st, mapi.PrivateFIDTasks, "IPM.Task", in.Summary, in)
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
	if old, err := strconv.ParseInt(r.PathValue("uid"), 10, 64); err == nil {
		_ = st.DeleteObject(old)
	}
	id, err := storeJSONItem(st, mapi.PrivateFIDTasks, "IPM.Task", in.Summary, in)
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
