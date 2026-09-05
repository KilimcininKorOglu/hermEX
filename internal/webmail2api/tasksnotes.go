package webmail2api

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxtask"
)

// taskJSON / noteJSON are the SPA's Task and Note shapes. A task maps to the
// canonical oxtask named properties (the one model ActiveSync, EWS, and a MAPI
// client share); a note maps to PR_SUBJECT (title) and PR_BODY (body).
type taskJSON struct {
	UID         string   `json:"uid"`
	Summary     string   `json:"summary"`
	Description string   `json:"description,omitempty"`
	Start       string   `json:"start,omitempty"` // YYYY-MM-DD (task start date)
	Due         string   `json:"due,omitempty"`
	Status      int      `json:"status"`             // 0 not started, 1 in progress, 2 complete, 3 waiting, 4 deferred
	Percent     int      `json:"percent"`            // 0..100 (% complete)
	Priority    int      `json:"priority,omitempty"` // 0=low, 1=normal, 2=high (PR_IMPORTANCE)
	Reminder    bool     `json:"reminder,omitempty"` // PidLidReminderSet
	Categories  []string `json:"categories,omitempty"`
	Recurrence  string   `json:"recurrence,omitempty"`  // RRULE string (FREQ=DAILY/WEEKLY/MONTHLY/YEARLY;...)
	Owner       string   `json:"owner,omitempty"`       // PidLidTaskOwner (current keeper)
	Assigner    string   `json:"assigner,omitempty"`    // PidLidTaskAssigner (last assigner)
	AcceptState int      `json:"acceptState,omitempty"` // PidLidTaskAcceptanceState: 0 not assigned/1 unknown/2 accepted/3 rejected
	Completed   bool     `json:"completed"`
}

type noteJSON struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Color int    `json:"color,omitempty"` // PidLidNoteColor: 0 blue, 1 green, 2 pink, 3 yellow, 4 white
	// LinkedMessageID annotates a mail: it holds that mail's
	// PR_INTERNET_MESSAGE_ID, so the note travels with the mail rather than with
	// the folder it happens to sit in. Empty on a free-standing note.
	LinkedMessageID string `json:"linkedMessageId,omitempty"`
}

// nameNoteLink is webmail's private named property holding the Message-ID of the
// mail a note annotates. The mail itself is never written to: an annotation must
// not change the message a mailbox may be sharing with someone else, and the
// Message-ID survives the mail being moved between folders, which its store id
// does not.
var nameNoteLink = mapi.PropertyName{Kind: mapi.MnidString, GUID: webmailNamespace, Name: "LinkedMessageID"}

// noteLinkTag resolves the note-link named property to a PtUnicode tag for this
// store, allocating its id when create is set (idempotent).
func noteLinkTag(st *objectstore.Store, create bool) (mapi.PropTag, error) {
	ids, err := st.GetNamedPropIDs(create, []mapi.PropertyName{nameNoteLink})
	if err != nil || len(ids) == 0 || ids[0] == 0 {
		return 0, err
	}
	return mapi.MakeTag(ids[0], mapi.PtUnicode), nil
}

// noteLinkOf reads the Message-ID a note annotates, or "" when it annotates none.
func noteLinkOf(st *objectstore.Store, msg *oxcmail.Message) string {
	tag, err := noteLinkTag(st, false)
	if err != nil || tag == 0 {
		return ""
	}
	return propString(msg, tag)
}

// fitsMAPILong reports whether a JSON integer fits the 32-bit MAPI long it is
// about to be stored in. JSON carries no width, so a value beyond int32 would
// otherwise wrap on assignment and land as a different, valid-looking setting.
func fitsMAPILong(v int) bool { return v >= math.MinInt32 && v <= math.MaxInt32 }

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
	t.Importance = in.Priority
	t.ReminderSet = in.Reminder
	t.Categories = in.Categories
	t.RecurrenceRule = in.Recurrence
	t.Owner = in.Owner
	t.Assigner = in.Assigner
	if in.AcceptState > 0 {
		t.AcceptanceState = in.AcceptState
	}
	// Status takes precedence when >=0 (0 is a valid value: not started). When
	// set, Complete derives from it (status 2 = complete); otherwise the legacy
	// Completed boolean drives status/percent in ToProps.
	if in.Status >= 0 {
		t.Status = in.Status
		t.Complete = in.Status == 2
	} else {
		t.Complete = in.Completed
	}
	if in.Percent > 0 {
		t.PercentComplete = float64(in.Percent) / 100.0
	}
	if in.Due != "" {
		if due, ok := parseDue(in.Due); ok {
			t.Due = due
		}
	}
	if in.Start != "" {
		if start, ok := parseDue(in.Start); ok {
			t.Start = start
		}
	}
	return t
}

func taskToJSON(t oxtask.Task) taskJSON {
	j := taskJSON{
		Summary:     t.Subject,
		Description: t.Body,
		Priority:    t.Importance,
		Reminder:    t.ReminderSet,
		Categories:  t.Categories,
		Recurrence:  t.RecurrenceRule,
		Owner:       t.Owner,
		Assigner:    t.Assigner,
		AcceptState: t.AcceptanceState,
	}
	if t.Status >= 0 {
		j.Status = t.Status
		j.Completed = t.Status == 2
	} else {
		j.Completed = t.Complete
	}
	if t.PercentComplete >= 0 {
		j.Percent = int(t.PercentComplete*100 + 0.5)
	}
	if !t.Due.IsZero() {
		j.Due = formatDue(t.Due)
	}
	if !t.Start.IsZero() {
		j.Start = formatDue(t.Start)
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

// noteColorTag resolves PidLidNoteColor (NameNoteColor, PSETID_Note) to a PtLong
// tag for this store, allocating its id when create is set.
func noteColorTag(st *objectstore.Store, create bool) (mapi.PropTag, error) {
	ids, err := st.GetNamedPropIDs(create, []mapi.PropertyName{mapi.NameNoteColor})
	if err != nil || len(ids) == 0 || ids[0] == 0 {
		return 0, err
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtLong)), nil
}

// noteColorOf reads PidLidNoteColor from the stored note; an unset named prop
// yields 3 (yellow, the Outlook default).
func noteColorOf(st *objectstore.Store, msg *oxcmail.Message) int {
	tag, err := noteColorTag(st, false)
	if err != nil || tag == 0 {
		return 3
	}
	if v, ok := msg.Props.Get(tag); ok {
		if n, ok := v.(int32); ok {
			return int(n)
		}
	}
	return 3
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
	// (status, percent complete) set by another protocol are not lost.
	merged := jsonToTask(in)
	if old, err := strconv.ParseInt(r.PathValue("uid"), 10, 64); err == nil {
		if msg, err := st.OpenMessage(old); err == nil {
			if prev, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs); err == nil {
				prev.Subject = merged.Subject
				prev.Body = merged.Body
				prev.Complete = merged.Complete
				prev.Due = merged.Due
				prev.Start = merged.Start
				prev.Importance = merged.Importance
				prev.ReminderSet = merged.ReminderSet
				prev.Categories = merged.Categories
				prev.Status = merged.Status
				prev.PercentComplete = merged.PercentComplete
				prev.RecurrenceRule = merged.RecurrenceRule
				prev.Owner = merged.Owner
				prev.Assigner = merged.Assigner
				prev.AcceptanceState = merged.AcceptanceState
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
	out := taskToJSON(merged)
	out.UID = strconv.FormatInt(id, 10)
	writeJSON(w, http.StatusOK, out)
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
			ID:              strconv.FormatInt(o.ID, 10),
			Title:           propString(msg, mapi.PrSubject),
			Body:            propString(msg, mapi.PrBody),
			Color:           noteColorOf(st, msg),
			LinkedMessageID: noteLinkOf(st, msg),
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
	// Color 0 means unset; an absent PidLidNoteColor reads back as yellow (3),
	// the Outlook default. A non-zero color is stamped explicitly.
	if in.Color != 0 && fitsMAPILong(in.Color) {
		if tag, err := noteColorTag(st, true); err == nil && tag != 0 {
			// #nosec G115 -- the fitsMAPILong guard on the same line refuses a value the property cannot carry
			props.Set(tag, int32(in.Color))
		}
	}
	setNoteLink(st, &props, in.LinkedMessageID)
	id, err := st.CreateMessage(mapi.PrivateFIDNotes, &oxcmail.Message{Props: props})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save note"})
		return
	}
	in.ID = strconv.FormatInt(id, 10)
	if in.Color == 0 {
		in.Color = 3
	}
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
	if in.Color != 0 && fitsMAPILong(in.Color) {
		if tag, err := noteColorTag(st, true); err == nil && tag != 0 {
			// #nosec G115 -- the fitsMAPILong guard on the same line refuses a value the property cannot carry
			props.Set(tag, int32(in.Color))
		}
	}
	setNoteLink(st, &props, in.LinkedMessageID)
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

// setNoteLink stamps the annotated mail's Message-ID onto a note's properties. An
// empty id leaves the property unset, which is a free-standing note.
func setNoteLink(st *objectstore.Store, props *mapi.PropertyValues, messageID string) {
	if messageID == "" {
		return
	}
	if tag, err := noteLinkTag(st, true); err == nil && tag != 0 {
		props.Set(tag, messageID)
	}
}

// handleGetMailNotes returns the notes annotating one mail. The mail is named by
// the SPA's opaque message id and its Message-ID is read server-side, so the
// browser never has to know or send it, and a mail that carries none (a draft, an
// item this server composed) simply has no notes.
func (s *Server) handleGetMailNotes(w http.ResponseWriter, r *http.Request) {
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	info, err := st.MessageByUID(fid, uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	msg, err := st.OpenMessage(info.ID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	notes := make([]noteJSON, 0)
	if link := propString(msg, mapi.PrInternetMessageID); link != "" {
		notes = notesLinkedTo(st, link)
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes})
}

// notesLinkedTo collects the mailbox's notes annotating the given Message-ID. A
// store that has never stored such a note resolves no tag and matches nothing.
func notesLinkedTo(st *objectstore.Store, messageID string) []noteJSON {
	out := make([]noteJSON, 0)
	tag, err := noteLinkTag(st, false)
	if err != nil || tag == 0 {
		return out
	}
	objs, _ := st.ListFolderObjects(mapi.PrivateFIDNotes)
	for _, o := range objs {
		msg, err := st.OpenMessage(o.ID)
		if err != nil {
			continue
		}
		if propString(msg, tag) != messageID {
			continue
		}
		out = append(out, noteJSON{
			ID:              strconv.FormatInt(o.ID, 10),
			Title:           propString(msg, mapi.PrSubject),
			Body:            propString(msg, mapi.PrBody),
			Color:           noteColorOf(st, msg),
			LinkedMessageID: messageID,
		})
	}
	return out
}

// mailLinkFor reads the Message-ID of one mail server-side. A non-zero status is
// the failure to report, with the reason to send back; the browser never handles
// the Message-ID and so cannot link a note to a mail it cannot open.
func mailLinkFor(st *objectstore.Store, fid int64, uid uint32) (link string, status int, reason string) {
	info, err := st.MessageByUID(fid, uid)
	if err != nil {
		return "", http.StatusNotFound, "not found"
	}
	msg, err := st.OpenMessage(info.ID)
	if err != nil {
		return "", http.StatusNotFound, "not found"
	}
	link = propString(msg, mapi.PrInternetMessageID)
	if link == "" {
		// A draft or an item this server composed, not received mail.
		return "", http.StatusBadRequest, "this message cannot be annotated"
	}
	return link, 0, ""
}

// noteProps builds the sticky note that carries the link back to the mail.
func noteProps(st *objectstore.Store, title, body string, color int, link string) mapi.PropertyValues {
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.StickyNote")
	props.Set(mapi.PrSubject, title)
	props.Set(mapi.PrBody, body)
	if color != 0 && fitsMAPILong(color) {
		if tag, err := noteColorTag(st, true); err == nil && tag != 0 {
			// #nosec G115 -- the fitsMAPILong guard on the same line refuses a value the property cannot carry
			props.Set(tag, int32(color))
		}
	}
	setNoteLink(st, &props, link)
	return props
}

// handleCreateMailNote annotates one mail. The mail is named by the SPA's opaque
// message id and its Message-ID is read server-side, the same way
// handleGetMailNotes reads it, so the browser never handles it and cannot link a
// note to a mail it cannot open. A mail carrying no Message-ID cannot be
// annotated; that is a draft or an item this server composed, not received mail.
func (s *Server) handleCreateMailNote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Body  string `json:"body"`
		Color int    `json:"color,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	st, fid, uid, ok := s.locate(w, r, in.ID)
	if !ok {
		return
	}
	defer st.Close()
	link, status, reason := mailLinkFor(st, fid, uid)
	if status != 0 {
		writeJSON(w, status, map[string]string{"error": reason})
		return
	}

	props := noteProps(st, in.Title, in.Body, in.Color, link)
	id, err := st.CreateMessage(mapi.PrivateFIDNotes, &oxcmail.Message{Props: props})
	if err != nil {
		logError("create-mail-note", err, logging.Fields{})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save note"})
		return
	}
	writeJSON(w, http.StatusOK, noteJSON{
		ID: strconv.FormatInt(id, 10), Title: in.Title, Body: in.Body,
		Color: in.Color, LinkedMessageID: link,
	})
}
