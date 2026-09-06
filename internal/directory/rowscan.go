package directory

import "database/sql"

// Every listing in this package repeats the same eleven lines: run the query,
// loop Next, scan into a value, append it, close on every error path, then check
// Err. Written out, that error handling is most of a listing function and hides
// the one line that says what a row means.
//
// queryRows carries the loop and the closing; the caller supplies only the scan.

// queryRows runs a query and collects one value per row.
func queryRows[T any](db *sql.DB, query string, args []any, scan func(*sql.Rows) (T, error)) ([]T, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// scanOne is the scan for a single-column query.
func scanOne[T any](rows *sql.Rows) (T, error) {
	var v T
	err := rows.Scan(&v)
	return v, err
}
