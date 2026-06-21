// Package relay performs outbound delivery of mail to recipients in domains this
// server is not authoritative for. Authenticated submission splits its
// recipients: local ones are filed into mailboxes, external ones are handed to
// this package's durable Spool. A background worker then drains the spool,
// resolving each recipient's MX and delivering over SMTP, retrying transient
// failures and bouncing permanent ones.
//
// The spool is the crash-survivable boundary. Once Enqueue returns, the message
// is the server's responsibility, so submission may answer 250 — an MTA must
// never accept a message and then silently drop it. State is per-recipient: one
// submission to several external recipients yields one row each, so a partial
// failure retries only the recipients that have not yet succeeded and never
// re-sends to those that have.
package relay

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"hermex/internal/migrate"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var spoolMigrationFS embed.FS

// spoolMigrations is the spool's schema history. v1 is the baseline; an existing
// unversioned spool adopts it as a no-op and records the version.
var spoolMigrations = migrate.MustLoadFS(spoolMigrationFS, "migrations")

// Spool is the durable outbound queue backed by a single SQLite database. A
// message row holds the raw bytes once; a recipient row per external address
// carries that address's own attempt count, next-eligible time, and last error.
type Spool struct {
	db *sql.DB
}

// Item is one due recipient delivery handed to the worker: the envelope sender,
// a single external recipient, and the raw message. RecipientID identifies the
// queue row so the worker can settle the attempt with Sent, Retry, or Fail.
type Item struct {
	RecipientID int64
	From        string
	Recipient   string
	Body        []byte
	Attempts    int
}

// QueueEntry is one queued recipient delivery as shown in the administrative
// mail-queue view: the message it belongs to, its envelope sender and single
// recipient, how many times delivery has been attempted, when it was enqueued and
// is next eligible, the last transient error (empty before the first attempt), and
// the message size. It is a read-only projection joined across the two tables.
type QueueEntry struct {
	RecipientID int64
	MessageID   int64
	From        string
	Recipient   string
	Attempts    int
	EnqueuedAt  time.Time
	NextAttempt time.Time
	LastError   string
	Size        int
}

// dsn mirrors the object store's connection string: a busy timeout, WAL
// journaling, enforced foreign keys, and FULL synchronous mode for durability.
func dsn(path string) string {
	return "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(FULL)"
}

// Open opens the relay spool at path, creating and initializing it if absent.
func Open(path string) (*Spool, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	s := &Spool{db: db}
	if err := s.ensureSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Spool) Close() error { return s.db.Close() }

func (s *Spool) ensureSchema() error {
	if err := migrate.Run(context.Background(),
		&migrate.SQLiteDriver{DB: s.db, Ver: migrate.UserVersion()}, 0, spoolMigrations); err != nil {
		return fmt.Errorf("relay: apply spool schema: %w", err)
	}
	return nil
}

// Enqueue durably stores body for delivery to each external recipient, all due
// immediately (now). It returns only after the data is committed, so the caller
// may then answer 250: the message has become the server's responsibility. An
// empty recipient list is a no-op.
func (s *Spool) Enqueue(from string, recipients []string, body []byte, now time.Time) error {
	if len(recipients) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO messages (envelope_from, body, enqueued_at) VALUES (?, ?, ?)`,
		from, body, now.Unix())
	if err != nil {
		return err
	}
	mid, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, rcpt := range recipients {
		if _, err := tx.Exec(
			`INSERT INTO recipients (message_id, recipient, next_attempt) VALUES (?, ?, ?)`,
			mid, rcpt, now.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Claim returns up to limit recipient deliveries whose next-attempt time has
// arrived (next_attempt <= now), oldest first, so the worker drains the backlog
// fairly.
func (s *Spool) Claim(now time.Time, limit int) ([]Item, error) {
	rows, err := s.db.Query(`
SELECT r.id, m.envelope_from, r.recipient, m.body, r.attempts
  FROM recipients r JOIN messages m ON m.id = r.message_id
 WHERE r.next_attempt <= ?
 ORDER BY r.next_attempt, r.id
 LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.RecipientID, &it.From, &it.Recipient, &it.Body, &it.Attempts); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// Sent removes a delivered recipient. When it was the message's last remaining
// recipient, the message row — and its body — is removed too.
func (s *Spool) Sent(recipientID int64) error { return s.settle(recipientID) }

// Fail removes a permanently undeliverable recipient. Like Sent it drops the
// message body once no recipient remains; the caller is responsible for emitting
// the bounce before calling it.
func (s *Spool) Fail(recipientID int64) error { return s.settle(recipientID) }

// Retry reschedules a recipient after a transient failure, recording the next
// eligible time, the incremented attempt count, and the error that deferred it.
func (s *Spool) Retry(recipientID int64, nextAttempt time.Time, lastErr string) error {
	_, err := s.db.Exec(
		`UPDATE recipients SET attempts = attempts + 1, next_attempt = ?, last_error = ? WHERE id = ?`,
		nextAttempt.Unix(), lastErr, recipientID)
	return err
}

// List returns every queued recipient delivery joined with its message, newest
// enqueue first, for the administrative mail-queue view. It reads all rows: the
// outbound spool is a transient backlog, not a mailbox, so no pagination.
func (s *Spool) List() ([]QueueEntry, error) {
	rows, err := s.db.Query(`
SELECT r.id, m.id, m.envelope_from, r.recipient, r.attempts,
       m.enqueued_at, r.next_attempt, r.last_error, LENGTH(m.body)
  FROM recipients r JOIN messages m ON m.id = r.message_id
 ORDER BY m.enqueued_at DESC, r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueEntry
	for rows.Next() {
		var e QueueEntry
		var enq, next int64
		if err := rows.Scan(&e.RecipientID, &e.MessageID, &e.From, &e.Recipient,
			&e.Attempts, &enq, &next, &e.LastError, &e.Size); err != nil {
			return nil, err
		}
		e.EnqueuedAt = time.Unix(enq, 0).UTC()
		e.NextAttempt = time.Unix(next, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// RetryNow makes a deferred recipient eligible for immediate delivery by moving
// its next-attempt time to when (the worker claims rows whose time has arrived).
// It is the administrative "flush" action; the attempt count and last error are
// kept so the delivery history is preserved. A recipient already gone is a no-op.
func (s *Spool) RetryNow(recipientID int64, when time.Time) error {
	_, err := s.db.Exec(`UPDATE recipients SET next_attempt = ? WHERE id = ?`,
		when.Unix(), recipientID)
	return err
}

// Delete removes a queued recipient at an administrator's request, dropping the
// message body once no recipient remains — without emitting a bounce (unlike
// Fail, the worker's permanent-failure path). A recipient already gone is a no-op.
func (s *Spool) Delete(recipientID int64) error { return s.settle(recipientID) }

// settle deletes a recipient row and, when its message has no recipients left,
// the message itself. A recipient already gone is treated as settled.
func (s *Spool) settle(recipientID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var mid int64
	if err := tx.QueryRow(`SELECT message_id FROM recipients WHERE id = ?`, recipientID).Scan(&mid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if _, err := tx.Exec(`DELETE FROM recipients WHERE id = ?`, recipientID); err != nil {
		return err
	}
	var remaining int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM recipients WHERE message_id = ?`, mid).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		if _, err := tx.Exec(`DELETE FROM messages WHERE id = ?`, mid); err != nil {
			return err
		}
	}
	return tx.Commit()
}
