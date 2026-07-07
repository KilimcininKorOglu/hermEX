package webmail2api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxtask"
)

// handleAssignTask assigns a stored task to a new owner and sends the assignee an
// IPM.TaskRequest message (MS-OXOTASK): the task is stamped Owner/Assigner/
// AcceptanceState=unknown, and an email describing the assignment is delivered to
// the assignee's Inbox via the shared mail delivery path, with a Sent copy for the
// owner. The full accept/decline response handling is a follow-on increment.
func (s *Server) handleAssignTask(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req struct {
		Assignee string `json:"assignee"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	assignee := strings.TrimSpace(req.Assignee)
	if assignee == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "assignee required"})
		return
	}
	st, _, ok := s.openStore(w, r)
	if !ok {
		return
	}
	defer st.Close()
	id, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	msg, err := st.OpenMessage(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	task, err := oxtask.FromProps(msg.Props, st.GetNamedPropIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read task"})
		return
	}
	// Stamp the assignment spine: the assignee becomes the Owner, the caller is the
	// Assigner, acceptance is unknown until the assignee responds.
	task.Owner = assignee
	task.Assigner = c.Email
	task.AcceptanceState = 1 // unknown (MS-OXOTASK PidLidTaskAcceptanceState)
	task.FCreator = true
	task.LastUpdate = time.Now().UTC()
	if err := st.DeleteObject(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not refile task"})
		return
	}
	newID, err := s.storeTask(st, task)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save task"})
		return
	}
	raw := buildTaskAssignmentMail(c.Email, assignee, task)
	if _, err := mta.DeliverAndRelay(s.accounts, s.spool, c.Email, []string{assignee}, raw, time.Now()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delivery failed: " + err.Error()})
		return
	}
	// File a Sent copy so the owner sees the outgoing assignment.
	_, _ = st.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, time.Now(), objectstore.FlagSeen)
	writeJSON(w, http.StatusOK, map[string]any{
		"uid":         strconv.FormatInt(newID, 10),
		"owner":       task.Owner,
		"assigner":    task.Assigner,
		"acceptState": task.AcceptanceState,
		"deliveredTo": assignee,
	})
}

// buildTaskAssignmentMail renders the IPM.TaskRequest message an assignee receives:
// a plain-text body describing the task and its owner, with the task's summary as the
// subject. v1 carries the task as descriptive text; the embedded TNEF task object the
// full MS-OXOTSDIR flow attaches is a follow-on.
func buildTaskAssignmentMail(owner, assignee string, t oxtask.Task) []byte {
	subject := t.Subject
	if subject == "" {
		subject = "Task assignment"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", owner)
	fmt.Fprintf(&b, "To: %s\r\n", assignee)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@hermex>\r\n", randomHex())
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	fmt.Fprintf(&b, "%s has assigned you a task.\r\n\r\n", owner)
	fmt.Fprintf(&b, "Title: %s\r\n", t.Subject)
	if t.Body != "" {
		fmt.Fprintf(&b, "\r\n%s\r\n", t.Body)
	}
	if !t.Due.IsZero() {
		fmt.Fprintf(&b, "\r\nDue: %s\r\n", t.Due.UTC().Format("2006-01-02"))
	}
	return []byte(b.String())
}
