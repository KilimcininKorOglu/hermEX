package objectstore

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxtask"
)

// legacyTaskBody is the task shape an early web client stored as JSON in a
// task's body instead of in the MS-OXOTASK named properties. Every field it ever
// wrote is listed, so a body holding anything else is not one of these.
type legacyTaskBody struct {
	UID         *string `json:"uid"`
	Summary     *string `json:"summary"`
	Description string  `json:"description"`
	Due         string  `json:"due"`
	Completed   bool    `json:"completed"`
}

// upgradeLegacyTasks rewrites each task still stored in the early JSON-body shape
// into the task model every protocol reads, in place. Until it runs, such a task
// shows its raw JSON as its description, no due date, and no priority on every
// surface. It costs one indexed read, plus one message read for each task that
// carries no task status, which a client writing the task model never leaves. A task that
// cannot be converted is recorded and left as it is, so the mailbox still opens.
func (s *Store) upgradeLegacyTasks() {
	ids, err := s.legacyTaskCandidates()
	if err != nil {
		s.logStoreError("upgrade_legacy_tasks", err)
		return
	}
	for _, id := range ids {
		if err := s.upgradeLegacyTask(id); err != nil {
			s.logStoreError("upgrade_legacy_task", err)
		}
	}
}

// legacyTaskCandidates returns the live tasks that carry no task status. The
// task model writes a status on every task, so only a task written outside it
// can lack one.
func (s *Store) legacyTaskCandidates() ([]int64, error) {
	ids, err := s.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameTaskStatus})
	if err != nil {
		return nil, err
	}
	status := int64(uint32(mapi.MakeTag(ids[0], mapi.PtLong)))
	rows, err := s.objdb.Query(`SELECT m.message_id FROM messages m
		WHERE m.parent_fid = ? AND m.is_deleted = 0 AND COALESCE(m.is_associated, 0) = 0
		  AND NOT EXISTS (SELECT 1 FROM message_properties p WHERE p.message_id = m.message_id AND p.proptag = ?)`,
		int64(mapi.PrivateFIDTasks), status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// upgradeLegacyTask converts one candidate when its body is the early JSON shape.
func (s *Store) upgradeLegacyTask(id int64) error {
	msg, err := s.OpenMessage(id)
	if err != nil {
		return err
	}
	task, ok := legacyTask(msg.Props)
	if !ok {
		return nil
	}
	props, err := oxtask.ToProps(task, s.GetNamedPropIDs)
	if err != nil {
		return err
	}
	return s.ModifyMessageProperties(id, props)
}

// legacyTask reads a task stored in the early JSON-body shape. ok is false for
// any other task.
func legacyTask(props mapi.PropertyValues) (oxtask.Task, bool) {
	class, _ := props.Get(mapi.PrMessageClass)
	body, _ := props.Get(mapi.PrBody)
	text, isText := body.(string)
	if class != oxtask.MessageClass || !isText || !strings.HasPrefix(text, "{") {
		return oxtask.Task{}, false
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	var in legacyTaskBody
	if err := dec.Decode(&in); err != nil || in.UID == nil || in.Summary == nil {
		return oxtask.Task{}, false
	}
	t := oxtask.New()
	t.Subject = *in.Summary
	t.Body = in.Description
	t.Complete = in.Completed
	t.Due = legacyDue(in.Due)
	return t, true
}

// legacyDue parses the early client's due date, a plain date or an RFC 3339
// instant, and yields the zero time for anything else.
func legacyDue(s string) time.Time {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
