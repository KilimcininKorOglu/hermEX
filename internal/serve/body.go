package serve

import (
	"errors"
	"io"
)

// ErrBodyTooLarge refuses a request body longer than the caller's cap.
var ErrBodyTooLarge = errors.New("request body over the size limit")

// ReadBody reads a whole request body of at most limit bytes. A longer body is
// refused with ErrBodyTooLarge rather than cut at the cap: a cut body's prefix can
// still parse, and the server would then act on a request it never fully read.
func ReadBody(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, ErrBodyTooLarge
	}
	return body, nil
}
