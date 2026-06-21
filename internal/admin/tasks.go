package admin

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"hermex/internal/directory"
)

// performLDAPSync runs the directory downsync for the default org and returns a
// human-readable result. A user whose mail domain is not provisioned locally is
// skipped rather than failing the whole sync. It is shared by the (async) enqueue
// path and the task worker so there is one sync implementation.
func (s *Server) performLDAPSync() (string, error) {
	if s.syncer == nil {
		return "", errors.New("directory sync is not available")
	}
	cfg, ok, err := s.dir.GetLDAPConfig(defaultOrgID)
	if err != nil || !ok {
		return "", errors.New("no directory is configured")
	}
	users, err := s.syncer.Sync(cfg)
	if err != nil {
		return "", err
	}
	var created, updated int
	for _, u := range users {
		isNew, err := s.dir.UpsertLDAPUser(u.Username, u.ExternID, s.paths.MaildirFor(u.Username))
		if err != nil {
			continue
		}
		if isNew {
			created++
		} else {
			updated++
		}
	}
	return fmt.Sprintf("Synced %d directory entries: %d created, %d updated.", len(users), created, updated), nil
}

// runTask executes one claimed task by type, returning its terminal status and a
// result message. An unknown type fails rather than silently succeeding.
func (s *Server) runTask(t directory.TaskInfo) (status, message string) {
	switch t.Type {
	case "ldapsync":
		msg, err := s.performLDAPSync()
		if err != nil {
			return directory.TaskFailed, "Sync failed: " + err.Error()
		}
		return directory.TaskDone, msg
	case "bayes-retrain":
		msg, err := s.performBayesRetrain()
		if err != nil {
			return directory.TaskFailed, "Retrain failed: " + err.Error()
		}
		return directory.TaskDone, msg
	default:
		return directory.TaskFailed, "unknown task type: " + t.Type
	}
}

// runNextTask claims and runs one pending task, recording its result, and reports
// whether a task ran. It is the unit the worker loop repeats and the tests drive
// directly.
func (s *Server) runNextTask() (bool, error) {
	t, ok, err := s.dir.ClaimNextTask()
	if err != nil || !ok {
		return false, err
	}
	status, message := s.runTask(t)
	return true, s.dir.FinishTask(t.ID, status, message)
}

// RunTaskWorker drains the admin task queue until ctx is cancelled, running
// pending tasks back-to-back and polling every poll interval when the queue is
// empty. A daemon starts exactly one worker.
func (s *Server) RunTaskWorker(ctx context.Context, poll time.Duration) {
	for {
		ran, err := s.runNextTask()
		if err != nil {
			log.Printf("hermex-admin: task worker: %v", err)
		}
		if ran {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}
