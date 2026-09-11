// Package netline reads one bounded line from a connection-oriented protocol
// stream. Every line-based mail protocol reads commands this way, and every one of
// them needs the same bound: a client that never sends a line terminator must not
// be able to grow the server's memory without limit, and it reaches these readers
// before it authenticates.
package netline

import (
	"bufio"
	"errors"
)

// ErrTooLong reports a line longer than the caller's limit. The rest of that line
// has already been discarded, so the next read starts at a line boundary and the
// caller can answer its protocol's error and keep serving the connection.
var ErrTooLong = errors.New("netline: line too long")

// ReadLine reads one line and returns it without the trailing CRLF or LF. A line
// longer than max is discarded to its end and reported as ErrTooLong. A max of 0
// or less reads without a bound, which only a test should ask for.
func ReadLine(br *bufio.Reader, max int) (string, error) {
	buf := make([]byte, 0, 128)
	for {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '\n' {
			if n := len(buf); n > 0 && buf[n-1] == '\r' {
				buf = buf[:n-1]
			}
			return string(buf), nil
		}
		// The limit counts the line itself, not its terminator, so the buffer is
		// allowed to reach one byte past it: that byte is the CR of a CRLF, which
		// is stripped above.
		if max > 0 && len(buf) > max {
			if err := discardLine(br); err != nil {
				return "", err
			}
			return "", ErrTooLong
		}
		buf = append(buf, b)
	}
}

// discardLine reads and drops bytes up to and including the next LF, so the stream
// resumes at a line boundary.
func discardLine(br *bufio.Reader) error {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return err
		}
		if b == '\n' {
			return nil
		}
	}
}
