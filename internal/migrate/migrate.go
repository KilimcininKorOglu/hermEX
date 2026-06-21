// Package migrate applies ordered, forward-only schema migrations to a database
// and records the version it has reached, so each change runs exactly once and
// an existing database is carried forward instead of being refused.
//
// It serves both engines hermEX uses, hiding their asymmetry behind Driver.
// SQLite DDL is transactional: a migration's steps and its version bump commit
// as one unit and roll back cleanly on failure. MySQL/MariaDB DDL is not: each
// step auto-commits and the version is recorded after the steps succeed, so a
// partial failure leaves the version unrecorded and must be safe to re-run
// (steps are single statements or idempotent). Migrations are forward only — a
// database recorded at a version higher than the binary supports is refused
// rather than silently downgraded, since data written under a newer schema may
// be unreadable.
package migrate

import (
	"context"
	"errors"
	"fmt"
)

// Migration is one schema version: the SQL statements that advance the database
// to Version, applied in slice order. Within a set, versions must be positive,
// unique, and strictly ascending; gaps are allowed.
type Migration struct {
	Version int
	Steps   []string
}

// Driver adapts the runner to a specific database and version store. Version is
// an unlocked read for the common already-current fast path. Lock acquires
// exclusive migration access and re-reads the version authoritatively. Apply
// runs one migration's steps and records its version while the lock is held.
// Unlock releases, committing when ok and otherwise rolling back where the
// engine supports it. A Driver value is single-use within one Run call and is
// not safe for concurrent use.
type Driver interface {
	Version(ctx context.Context) (int, error)
	Lock(ctx context.Context) (int, error)
	Apply(ctx context.Context, m Migration) error
	Unlock(ctx context.Context, ok bool) error
}

// ErrDowngrade reports a database recorded at a higher version than this binary
// supports; opening it could corrupt data written under a schema the binary
// does not understand, so it is refused.
var ErrDowngrade = errors.New("migrate: database schema is newer than this binary")

// Run brings the database to the highest of baseline and the migration set,
// applying every pending migration once in ascending order. baseline is the
// version a freshly created database already sits at without any migration (for
// stores whose creation path stamps a version directly, such as the object
// store); pass 0 when the migration set itself carries the initial schema. Run
// is safe to call on every open: once the database has reached the target it
// takes no lock and does no work.
func Run(ctx context.Context, d Driver, baseline int, migs []Migration) error {
	if err := validate(migs); err != nil {
		return err
	}
	target := baseline
	for _, m := range migs {
		if m.Version > target {
			target = m.Version
		}
	}

	// Fast path: an unlocked read avoids taking the write lock on every open
	// once the database has reached the target — the overwhelmingly common case.
	cur, err := d.Version(ctx)
	if err != nil {
		return err
	}
	if cur > target {
		return fmt.Errorf("%w: at version %d, this binary supports %d", ErrDowngrade, cur, target)
	}
	if cur == target {
		return nil
	}

	// Slow path: take the lock and re-read the version inside it, so two
	// processes racing to migrate the same database serialize and the loser sees
	// the already-applied version rather than re-applying the steps.
	cur, err = d.Lock(ctx)
	if err != nil {
		return err
	}
	err = apply(ctx, d, migs, cur, target)
	if uerr := d.Unlock(ctx, err == nil); uerr != nil && err == nil {
		err = uerr
	}
	return err
}

func apply(ctx context.Context, d Driver, migs []Migration, cur, target int) error {
	if cur > target {
		return fmt.Errorf("%w: at version %d, this binary supports %d", ErrDowngrade, cur, target)
	}
	for _, m := range migs {
		if m.Version <= cur {
			continue
		}
		if err := d.Apply(ctx, m); err != nil {
			return fmt.Errorf("migrate: apply v%d: %w", m.Version, err)
		}
	}
	return nil
}

// validate rejects a malformed set so the apply order is unambiguous: versions
// must be positive and strictly ascending (which also makes them unique).
func validate(migs []Migration) error {
	prev := 0
	for _, m := range migs {
		if m.Version <= 0 {
			return fmt.Errorf("migrate: invalid version %d (must be positive)", m.Version)
		}
		if m.Version <= prev {
			return fmt.Errorf("migrate: version %d out of order (must be strictly ascending)", m.Version)
		}
		prev = m.Version
	}
	return nil
}
