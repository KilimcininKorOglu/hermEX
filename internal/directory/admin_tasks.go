package directory

import (
	"database/sql"
	"errors"
	"time"
)

// Async admin task status values.
const (
	TaskPending = "pending"
	TaskRunning = "running"
	TaskDone    = "done"
	TaskFailed  = "failed"
)

// TaskInfo is one async admin task: a long-running operation (LDAP directory
// sync, domain purge) tracked so the admin Task queue page can show its progress.
// Params carries the operation's argument (an org or domain id as a string);
// Message holds the result summary or the failure reason.
type TaskInfo struct {
	ID        int64
	Type      string
	Status    string
	Params    string
	Message   string
	CreatedBy string
	CreatedAt int64
	UpdatedAt int64
}

// CreateTask enqueues a pending async task and returns its id.
func (d *SQLDirectory) CreateTask(taskType, params, createdBy string) (int64, error) {
	now := time.Now().Unix()
	res, err := d.db.Exec(
		`INSERT INTO admin_tasks (task_type, status, params, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		taskType, TaskPending, params, createdBy, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListTasks returns the most recent tasks, newest first, capped at limit (default
// 100 when limit <= 0).
func (d *SQLDirectory) ListTasks(limit int) ([]TaskInfo, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.db.Query(
		`SELECT id, task_type, status, params, message, created_by, created_at, updated_at
		   FROM admin_tasks ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskInfo
	for rows.Next() {
		var t TaskInfo
		if err := rows.Scan(&t.ID, &t.Type, &t.Status, &t.Params, &t.Message, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTask returns one task by id.
func (d *SQLDirectory) GetTask(id int64) (TaskInfo, bool, error) {
	var t TaskInfo
	err := d.db.QueryRow(
		`SELECT id, task_type, status, params, message, created_by, created_at, updated_at
		   FROM admin_tasks WHERE id = ?`, id).Scan(
		&t.ID, &t.Type, &t.Status, &t.Params, &t.Message, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskInfo{}, false, nil
	}
	if err != nil {
		return TaskInfo{}, false, err
	}
	return t, true, nil
}

// ClaimNextTask atomically takes the oldest pending task, marks it running, and
// returns it; ok is false when nothing is pending. The SELECT ... FOR UPDATE in a
// transaction makes the claim safe against a second worker racing for the same row.
func (d *SQLDirectory) ClaimNextTask() (TaskInfo, bool, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return TaskInfo{}, false, err
	}
	defer tx.Rollback()
	var t TaskInfo
	err = tx.QueryRow(
		`SELECT id, task_type, status, params, message, created_by, created_at, updated_at
		   FROM admin_tasks WHERE status = ? ORDER BY id LIMIT 1 FOR UPDATE`, TaskPending).Scan(
		&t.ID, &t.Type, &t.Status, &t.Params, &t.Message, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskInfo{}, false, nil
	}
	if err != nil {
		return TaskInfo{}, false, err
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE admin_tasks SET status = ?, updated_at = ? WHERE id = ?`, TaskRunning, now, t.ID); err != nil {
		return TaskInfo{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return TaskInfo{}, false, err
	}
	t.Status, t.UpdatedAt = TaskRunning, now
	return t, true, nil
}

// FinishTask records a task's terminal status (done or failed) and its result or
// error message, which is truncated to the column width.
func (d *SQLDirectory) FinishTask(id int64, status, message string) error {
	if len(message) > 512 {
		message = message[:512]
	}
	_, err := d.db.Exec(
		`UPDATE admin_tasks SET status = ?, message = ?, updated_at = ? WHERE id = ?`,
		status, message, time.Now().Unix(), id)
	return err
}
