package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// mysqlMigrateLock is the named advisory lock that serializes concurrent
// migration runners against one MySQL/MariaDB database (GET_LOCK is
// connection-scoped, so the whole run holds one pinned connection).
const mysqlMigrateLock = "hermex_schema_migrate"

// schemaMigrationsDDL creates the bookkeeping table that records which versions
// have been applied. It is idempotent so adopting an existing database is safe.
const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INT UNSIGNED NOT NULL,
	applied_at BIGINT NOT NULL,
	PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

// MySQLDriver migrates a MySQL/MariaDB database, recording applied versions in a
// schema_migrations table. DDL there is not transactional, so each migration's
// steps run and then its version is recorded; a partial failure leaves the
// version unrecorded and is safe to re-run (steps must be single statements or
// idempotent). Concurrent runners serialize on a named advisory lock.
type MySQLDriver struct {
	DB *sql.DB

	conn *sql.Conn
}

// Version ensures the bookkeeping table exists and reads the highest applied
// version (0 when none) without taking the advisory lock.
func (d *MySQLDriver) Version(ctx context.Context) (int, error) {
	if _, err := d.DB.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return 0, err
	}
	return maxVersion(ctx, d.DB)
}

// Lock takes the advisory lock on a pinned connection, ensures the bookkeeping
// table, and re-reads the highest applied version under the lock.
func (d *MySQLDriver) Lock(ctx context.Context) (int, error) {
	c, err := d.DB.Conn(ctx)
	if err != nil {
		return 0, err
	}
	var got sql.NullInt64
	if err := c.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", mysqlMigrateLock, 10).Scan(&got); err != nil {
		c.Close()
		return 0, err
	}
	if got.Int64 != 1 {
		c.Close()
		return 0, fmt.Errorf("migrate: could not acquire advisory lock %q", mysqlMigrateLock)
	}
	d.conn = c
	if _, err := c.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		_ = d.Unlock(ctx, false)
		return 0, err
	}
	v, err := maxVersion(ctx, c)
	if err != nil {
		_ = d.Unlock(ctx, false)
		return 0, err
	}
	return v, nil
}

// Apply runs the migration's steps (each auto-committing) and then records its
// version, so a partial failure re-runs safely.
func (d *MySQLDriver) Apply(ctx context.Context, m Migration) error {
	for _, stmt := range m.Steps {
		if _, err := d.conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	_, err := d.conn.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)", m.Version, time.Now().Unix())
	return err
}

// Unlock releases the advisory lock and the connection. The ok flag is ignored:
// MySQL DDL has already auto-committed, so there is nothing to roll back.
func (d *MySQLDriver) Unlock(ctx context.Context, ok bool) error {
	if d.conn == nil {
		return nil
	}
	c := d.conn
	d.conn = nil
	_, err := c.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", mysqlMigrateLock)
	if cerr := c.Close(); err == nil {
		err = cerr
	}
	return err
}

// queryer is satisfied by both *sql.DB and *sql.Conn.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func maxVersion(ctx context.Context, q queryer) (int, error) {
	var v sql.NullInt64
	if err := q.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}
