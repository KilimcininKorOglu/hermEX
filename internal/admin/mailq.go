package admin

import (
	"net/http"
	"strconv"
	"time"

	"hermex/internal/relay"
)

// MailQueue is the administrative view onto the outbound relay spool: list the
// queued recipient deliveries, flush a deferred one to immediate retry, or drop
// one. The concrete relaySpool satisfies it; tests substitute a real spool at a
// temp path (the spool is plain SQLite, so no fake is needed). It is the native
// equivalent of a postfix-style mail-queue page — hermEX has no postfix, so it
// reads its own durable relay spool instead.
type MailQueue interface {
	List() ([]relay.QueueEntry, error)
	RetryNow(recipientID int64) error
	Delete(recipientID int64) error
}

// relaySpool is the production MailQueue: it opens the shared relay spool at the
// configured path for each call and closes it before returning, matching how the
// mailbox store accesses per-mailbox stores (SQLite WAL handles the concurrency
// with the relay worker that drains the same spool). It holds no state.
type relaySpool struct{ path string }

func (rs relaySpool) List() ([]relay.QueueEntry, error) {
	sp, err := relay.Open(rs.path)
	if err != nil {
		return nil, err
	}
	defer sp.Close()
	return sp.List()
}

func (rs relaySpool) RetryNow(recipientID int64) error {
	sp, err := relay.Open(rs.path)
	if err != nil {
		return err
	}
	defer sp.Close()
	return sp.RetryNow(recipientID, time.Now())
}

func (rs relaySpool) Delete(recipientID int64) error {
	sp, err := relay.Open(rs.path)
	if err != nil {
		return err
	}
	defer sp.Close()
	return sp.Delete(recipientID)
}

// mailqView is one queued delivery rendered for the admin page: the identity and
// envelope, a derived status, and human-readable times and size.
type mailqView struct {
	ID          int64
	From        string
	Recipient   string
	Attempts    int
	Enqueued    string
	NextAttempt string
	Status      string
	LastError   string
	SizeKB      int
}

// mailqViews reads the queue and projects each entry for display. An entry that
// has been attempted (has a recorded error) is "Deferred" and shows its next
// retry time; one awaiting its first attempt is "Pending".
func (s *Server) mailqViews() ([]mailqView, error) {
	entries, err := s.mailq.List()
	if err != nil {
		return nil, err
	}
	out := make([]mailqView, 0, len(entries))
	for _, e := range entries {
		status := "Pending"
		next := ""
		if e.Attempts > 0 || e.LastError != "" {
			status = "Deferred"
			next = e.NextAttempt.Format("2006-01-02 15:04:05")
		}
		out = append(out, mailqView{
			ID: e.RecipientID, From: e.From, Recipient: e.Recipient, Attempts: e.Attempts,
			Enqueued: e.EnqueuedAt.Format("2006-01-02 15:04:05"), NextAttempt: next,
			Status: status, LastError: e.LastError, SizeKB: (e.Size + 1023) / 1024,
		})
	}
	return out, nil
}

// handleUIMailq renders the outbound mail-queue page (system admins; read-only
// tier may view).
func (s *Server) handleUIMailq(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	views, err := s.mailqViews()
	errMsg := ""
	if err != nil {
		errMsg = "Could not read the mail queue: " + err.Error()
	}
	s.render(w, "mailq.html", map[string]any{
		"Nav": "mailq", "CSRF": csrfCookieValue(r), "Queue": views, "Error": errMsg,
	})
}

// handleUIMailqPanel renders just the queue table (refresh / post-action swap).
func (s *Server) handleUIMailqPanel(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	s.renderMailqPanel(w, r, "")
}

// handleUIMailqRetry flushes a deferred entry to immediate retry, then re-renders
// the queue panel.
func (s *Server) handleUIMailqRetry(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	errMsg := ""
	if err := s.mailq.RetryNow(id); err != nil {
		errMsg = "Could not flush entry: " + err.Error()
	}
	s.renderMailqPanel(w, r, errMsg)
}

// handleUIMailqDelete drops a queued entry (no bounce), then re-renders the panel.
func (s *Server) handleUIMailqDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	errMsg := ""
	if err := s.mailq.Delete(id); err != nil {
		errMsg = "Could not delete entry: " + err.Error()
	}
	s.renderMailqPanel(w, r, errMsg)
}

// renderMailqPanel renders the queue table partial with the current entries and an
// optional error banner.
func (s *Server) renderMailqPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	views, err := s.mailqViews()
	if err != nil && errMsg == "" {
		errMsg = "Could not read the mail queue: " + err.Error()
	}
	s.render(w, "mailq-panel", map[string]any{
		"CSRF": csrfCookieValue(r), "Queue": views, "Error": errMsg,
	})
}

// handleGetMailq returns the queue as JSON (system admins).
func (s *Server) handleGetMailq(w http.ResponseWriter, r *http.Request) {
	entries, err := s.mailq.List()
	if err != nil {
		http.Error(w, "could not read the mail queue", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []relay.QueueEntry{}
	}
	writeJSON(w, entries)
}

// handleRetryMailq flushes one queued entry to immediate retry (system admins).
func (s *Server) handleRetryMailq(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.mailq.RetryNow(id); err != nil {
		http.Error(w, "could not flush entry", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteMailq drops one queued entry without a bounce (system admins).
func (s *Server) handleDeleteMailq(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.mailq.Delete(id); err != nil {
		http.Error(w, "could not delete entry", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
