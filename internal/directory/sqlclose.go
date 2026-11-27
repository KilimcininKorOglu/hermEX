package directory

import (
	"database/sql"
	"errors"
	"io"
)

// closeRows closes rows and joins a close failure into *err, so a deferred close
// that loses a result reaches the caller instead of being dropped.
func closeRows(rows *sql.Rows, err *error) {
	closeInto(rows, err)
}

// closeInto closes c and joins a close failure into *err.
func closeInto(c io.Closer, err *error) {
	if cerr := c.Close(); cerr != nil {
		*err = errors.Join(*err, cerr)
	}
}

// rollbackTx rolls tx back unless it already committed, joining a rollback
// failure into *err.
func rollbackTx(tx *sql.Tx, err *error) {
	if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
		*err = errors.Join(*err, rerr)
	}
}
