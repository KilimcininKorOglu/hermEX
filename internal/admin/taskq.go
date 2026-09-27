package admin

import (
	"net/http"

	"hermex/internal/directory"
)

// taskView is one async task rendered for the Task queue page.
type taskView struct {
	ID        int64
	Type      string
	Status    string
	CreatedBy string
	Message   string
	Created   stamp
	Updated   stamp
}

// taskViews reads the most recent tasks and projects them for display.
func (s *Server) taskViews(c clock) ([]taskView, error) {
	tasks, err := s.dir.ListTasks(100)
	if err != nil {
		return nil, err
	}
	out := make([]taskView, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskView{
			ID: t.ID, Type: t.Type, Status: t.Status, CreatedBy: t.CreatedBy, Message: t.Message,
			Created: c.unix(t.CreatedAt), Updated: c.unix(t.UpdatedAt),
		})
	}
	return out, nil
}

// handleUITaskq renders the Task queue page (system admins; read-only tier may
// view).
func (s *Server) handleUITaskq(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	data := s.taskqPanelData(r)
	data["Nav"] = "taskq"
	s.render(w, r, "taskq.html", data)
}

// handleUITaskqPanel renders just the task table (the page polls it to refresh).
func (s *Server) handleUITaskqPanel(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.render(w, r, "taskq-panel", s.taskqPanelData(r))
}

// taskqPanelData returns what the task table renders. A failed read is reported in
// the table on every poll, so it never reads as an empty queue.
func (s *Server) taskqPanelData(r *http.Request) map[string]any {
	views, err := s.taskViews(requestClock(r))
	return map[string]any{"Tasks": views, "TasksError": s.listFailure("what.taskQueue", err)}
}

// handleGetTaskqStatus reports whether the worker has work: running tasks and the
// pending count (system admins), the native equivalent of the reference's tasq
// status endpoint.
func (s *Server) handleGetTaskqStatus(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.dir.ListTasks(1000)
	if err != nil {
		s.fail(w, "could not read tasks", err, http.StatusInternalServerError)
		return
	}
	var pending, running int
	for _, t := range tasks {
		switch t.Status {
		case directory.TaskPending:
			pending++
		case directory.TaskRunning:
			running++
		}
	}
	writeJSON(w, map[string]any{"running": running > 0, "active": running, "pending": pending})
}
