package webmail2api

import (
	"strings"
	"unicode"
)

// logSafeMax bounds a logged value. The stub handler answers every unrouted API
// path, so without a cap one request writes as much of the log as it likes.
const logSafeMax = 256

// logSafe renders a client-controlled value safe to write to a line-oriented log.
//
// A request path reaches the handler percent-decoded, so "%0a" arrives as a real
// newline: written verbatim it ends the entry and starts one the caller wrote,
// which reads exactly like a genuine line. The rest of the C0 range plus DEL are
// the same problem for a terminal reading the file, ESC in particular, so the whole
// class is dropped rather than just CR and LF.
//
// Printable text, including non-ASCII, is left alone: a sanitizer that mangles
// ordinary paths makes the log answer less than it did before.
func logSafe(s string) string {
	if len(s) > logSafeMax {
		s = s[:logSafeMax] + "..."
	}
	return strings.Map(func(r rune) rune {
		// C0, DEL and C1 (U+0085 NEL is a line break, U+009B a terminal CSI),
		// plus the Unicode line and paragraph separators.
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, s)
}
