package admin

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"hermex/internal/directory"
	"hermex/internal/ldapsync"
)

// performLDAPSync runs the directory downsync of the bindings a task names and
// returns a human-readable result. params is one binding id; an empty params (a task
// queued before bindings existed) means every binding. Each binding syncs only its
// own domain. It is shared by the (async) enqueue path and the task worker so there
// is one sync implementation.
func (s *Server) performLDAPSync(params string) (string, error) {
	if s.syncer == nil {
		return "", errors.New("directory sync is not available")
	}
	ids, err := s.ldapSyncTargets(params)
	if err != nil {
		return "", err
	}
	return ldapsync.RunBindings(ids, s.dir, s.syncer, s.dir, s.paths.MaildirFor,
		func(f string, a ...any) { log.Printf("ldapsync: "+f, a...) })
}

// ldapSyncTargets resolves a sync task's params to the binding ids it covers.
func (s *Server) ldapSyncTargets(params string) ([]int64, error) {
	if params != "" {
		id, err := strconv.ParseInt(params, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("malformed binding id %q: %w", params, err)
		}
		return []int64{id}, nil
	}
	bindings, err := s.dir.ListLDAPBindings(0)
	if err != nil {
		return nil, fmt.Errorf("read the directory bindings: %w", err)
	}
	if len(bindings) == 0 {
		return nil, errors.New("no domain is bound to a directory")
	}
	ids := make([]int64, len(bindings))
	for i, b := range bindings {
		ids[i] = b.ID
	}
	return ids, nil
}

// runTask executes one claimed task by type, returning its terminal status and a
// result message. An unknown type fails rather than silently succeeding.
func (s *Server) runTask(t directory.TaskInfo) (status, message string) {
	switch t.Type {
	case "ldapsync":
		msg, err := s.performLDAPSync(t.Params)
		if err != nil {
			return directory.TaskFailed, s.notice("tasks.syncFailed", err)
		}
		return directory.TaskDone, msg
	case "bayes-retrain":
		msg, err := s.performBayesRetrain()
		if err != nil {
			return directory.TaskFailed, s.notice("tasks.retrainFailed", err)
		}
		return directory.TaskDone, msg
	default:
		return directory.TaskFailed, msg("tasks.unknownType", t.Type)
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
