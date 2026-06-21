package migrate

import (
	"context"
	"database/sql"
	"fmt"
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
func (d *SQLiteDriver) Version(ctx context.Context) (int, error) {
	c, err := d.DB.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer c.Close()
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
		c.Close()
		return 0, err
	}
	d.conn = c
	v, err := d.Ver.Read(ctx, c)
	if err != nil {
		_ = d.Unlock(ctx, false)
		return 0, err
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
func (d *SQLiteDriver) Unlock(ctx context.Context, ok bool) error {
	if d.conn == nil {
		return nil
	}
	c := d.conn
	d.conn = nil
	verb := "ROLLBACK"
	if ok {
		verb = "COMMIT"
	}
	_, err := c.ExecContext(ctx, verb)
	if cerr := c.Close(); err == nil {
		err = cerr
	}
	return err
}
