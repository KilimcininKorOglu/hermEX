package admin

import (
	"net/http"
	"time"

	"hermex/internal/directory"
)

// taskView is one async task rendered for the Task queue page.
type taskView struct {
	ID        int64
	Type      string
	Status    string
	CreatedBy string
	Message   string
	Created   string
	Updated   string
}

// taskTime formats a Unix timestamp for display, or "" when zero.
func taskTime(s int64) string {
	if s == 0 {
		return ""
	}
	return time.Unix(s, 0).Format("2006-01-02 15:04:05")
}

// taskViews reads the most recent tasks and projects them for display.
func (s *Server) taskViews() ([]taskView, error) {
	tasks, err := s.dir.ListTasks(100)
	if err != nil {
		return nil, err
	}
	out := make([]taskView, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskView{
			ID: t.ID, Type: t.Type, Status: t.Status, CreatedBy: t.CreatedBy, Message: t.Message,
			Created: taskTime(t.CreatedAt), Updated: taskTime(t.UpdatedAt),
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
	views, err := s.taskViews()
	errMsg := ""
	if err != nil {
		errMsg = "Could not read the task queue: " + err.Error()
	}
	s.render(w, "taskq.html", map[string]any{"Nav": "taskq", "Tasks": views, "Error": errMsg})
}

// handleUITaskqPanel renders just the task table (the page polls it to refresh).
func (s *Server) handleUITaskqPanel(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	views, _ := s.taskViews()
	s.render(w, "taskq-panel", map[string]any{"Tasks": views})
}

// handleGetTaskqStatus reports whether the worker has work: running tasks and the
// pending count (system admins), the native equivalent of the reference's tasq
// status endpoint.
func (s *Server) handleGetTaskqStatus(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.dir.ListTasks(1000)
	if err != nil {
		http.Error(w, "could not read tasks", http.StatusInternalServerError)
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
