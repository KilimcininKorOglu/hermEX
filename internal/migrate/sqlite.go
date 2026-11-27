package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
)

// SQLiteVersion reads and writes a SQLite database's recorded schema version.
// Two stores are in use: the built-in PRAGMA user_version (the IMAP index and
// the relay spool) and a row in an application table (the object store's
// configurations table). Read and Write operate on the migration connection, so
// Write participates in the migration transaction.
type SQLiteVersion struct {
	Read  func(ctx context.Context, c *sql.Conn) (int, error)
	Write func(ctx context.Context, c *sql.Conn, v int) error
}

// UserVersion is the SQLiteVersion backed by the built-in PRAGMA user_version,
// which is part of the database header and so rolls back with the transaction.
func UserVersion() SQLiteVersion {
	return SQLiteVersion{
		Read: func(ctx context.Context, c *sql.Conn) (int, error) {
			var v int
			err := c.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
			return v, err
		},
		Write: func(ctx context.Context, c *sql.Conn, v int) error {
			// PRAGMA does not accept bound parameters; v is a trusted int.
			_, err := c.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", v))
			return err
		},
	}
}

// SQLiteDriver migrates a SQLite database. The fast-path version read uses a
// pooled connection; the slow path pins one connection and opens an immediate
// write transaction, so concurrent openers serialize on the connection's busy
// timeout and the migration's steps and version bump commit together.
type SQLiteDriver struct {
	DB  *sql.DB
	Ver SQLiteVersion

	conn *sql.Conn
}

// Version reads the recorded version without taking the migration lock.
func (d *SQLiteDriver) Version(ctx context.Context) (v int, err error) {
	c, err := d.DB.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		if cerr := c.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()
	return d.Ver.Read(ctx, c)
}

// Lock pins a connection, opens an immediate write transaction, and re-reads the
// version inside the lock.
func (d *SQLiteDriver) Lock(ctx context.Context) (int, error) {
	c, err := d.DB.Conn(ctx)
	if err != nil {
		return 0, err
	}
	if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, errors.Join(err, c.Close())
	}
	d.conn = c
	v, err := d.Ver.Read(ctx, c)
	if err != nil {
		return 0, errors.Join(err, d.Unlock(ctx, false))
	}
	return v, nil
}

// Apply runs the migration's steps and records its version within the open
// transaction.
func (d *SQLiteDriver) Apply(ctx context.Context, m Migration) error {
	for _, stmt := range m.Steps {
		if _, err := d.conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return d.Ver.Write(ctx, d.conn, m.Version)
}

// Unlock commits (ok) or rolls back the transaction and releases the connection.
// A COMMIT can fail and leave the transaction open (a deferred foreign key still
// violated at commit), and Close returns the connection to the pool rather than
// closing it, so a failed COMMIT is rolled back; a connection whose transaction
// cannot be ended is discarded instead of handed to the next caller.
func (d *SQLiteDriver) Unlock(ctx context.Context, ok bool) error {
	if d.conn == nil {
		return nil
	}
	c := d.conn
	d.conn = nil
	var err error
	if ok {
		err = execOn(ctx, c, "COMMIT")
	}
	if !ok || err != nil {
		if rerr := execOn(ctx, c, "ROLLBACK"); rerr != nil && !strings.Contains(rerr.Error(), "no transaction is active") {
			err = errors.Join(err, rerr, discard(c))
		}
	}
	if cerr := c.Close(); err == nil {
		err = cerr
	}
	return err
}

func execOn(ctx context.Context, c *sql.Conn, stmt string) error {
	_, err := c.ExecContext(ctx, stmt)
	return err
}

// discard marks c bad, so the pool closes it instead of reusing it.
func discard(c *sql.Conn) error {
	err := c.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) {
		return nil
	}
	return err
}
