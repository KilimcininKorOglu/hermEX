package store

import "errors"

// ErrNotFound is returned when a requested folder or message does not exist.
var ErrNotFound = errors.New("store: not found")
