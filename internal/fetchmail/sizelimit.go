package fetchmail

import (
	"errors"
	"sync/atomic"
)

// defaultMaxMessage bounds one fetched message when no operator limit is set. A
// remote source decides how many bytes it sends, and both mini-clients used to
// buffer whatever arrived, so this is the ceiling that keeps one message from
// costing the worker its memory. It is generous enough that no legitimate
// message reaches it.
const defaultMaxMessage = 64 << 20

// ErrMessageTooLarge reports a source message over the cap. The message is left
// on the source server: refusing it is better than dropping it silently, and an
// operator who wants it can raise the limit.
var ErrMessageTooLarge = errors.New("fetchmail: message exceeds the size limit")

// maxMessageBytes is the operator's per-message cap; 0 selects defaultMaxMessage.
// The worker is a per-process singleton, so a package-level value is the right
// scope (mirrors the other daemons' size-limit setters).
var maxMessageBytes atomic.Int64

// SetMaxMessage sets the largest message the fetch clients will read, in bytes.
// 0 restores the built-in default, and a negative value is treated as 0: "no
// operator limit" must still leave a ceiling, because the point of the cap is to
// bound an allocation a remote server controls.
func SetMaxMessage(n int64) {
	if n < 0 {
		n = 0
	}
	maxMessageBytes.Store(n)
}

// maxMessage returns the cap in force.
func maxMessage() int64 {
	if n := maxMessageBytes.Load(); n > 0 {
		return n
	}
	return defaultMaxMessage
}
