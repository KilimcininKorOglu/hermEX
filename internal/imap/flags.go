// Package imap implements an RFC 3501 IMAP4rev1 server backed by the mailbox
// store. The binding contract is RFC 3501 and real clients (Thunderbird), not
// any particular server's internal implementation.
package imap

import (
	"strings"

	"hermex/internal/store"
)

// systemFlag pairs an IMAP system-flag name with its persisted store bit.
type systemFlag struct {
	name string
	bit  int64
}

// systemFlags is the ordered set of persisted system flags. The order is the
// one used in FLAGS / PERMANENTFLAGS responses. \Recent is deliberately absent:
// it is per-session state (RFC 3501 §2.3.2), set by the server on new arrivals
// and never settable by the client, so it is not stored.
var systemFlags = []systemFlag{
	{`\Seen`, store.FlagSeen},
	{`\Answered`, store.FlagAnswered},
	{`\Flagged`, store.FlagFlagged},
	{`\Deleted`, store.FlagDeleted},
	{`\Draft`, store.FlagDraft},
}

// flagBit maps an IMAP system-flag name to its store bit. Flag names are
// case-insensitive (RFC 3501 §9, "flag" is atom-based and \Seen == \SEEN). ok
// is false for keywords and unknown flags, which this server does not persist.
func flagBit(name string) (bit int64, ok bool) {
	for _, f := range systemFlags {
		if strings.EqualFold(f.name, name) {
			return f.bit, true
		}
	}
	return 0, false
}

// formatFlags renders a stored flag set as a space-separated IMAP flag list
// (without the enclosing parentheses), appending \Recent when recent is true.
func formatFlags(flags int64, recent bool) string {
	var names []string
	for _, f := range systemFlags {
		if flags&f.bit != 0 {
			names = append(names, f.name)
		}
	}
	if recent {
		names = append(names, `\Recent`)
	}
	return strings.Join(names, " ")
}

// supportedFlagNames returns the system flags advertised in a SELECT/EXAMINE
// FLAGS response (the set a client may use; \Recent is reported separately as a
// PERMANENTFLAGS detail by the server, never advertised as settable here).
func supportedFlagNames() string {
	names := make([]string, len(systemFlags))
	for i, f := range systemFlags {
		names[i] = f.name
	}
	return strings.Join(names, " ")
}

// applyFlagNames folds a list of flag names into base according to an IMAP
// STORE operation: mode '+' sets the named bits, '-' clears them, ' ' (replace)
// produces exactly the named bits. Unknown names (keywords) are ignored rather
// than failing the STORE; the server's post-STORE FLAGS response then truthfully
// reports the bits that actually stuck.
func applyFlagNames(base int64, mode byte, names []string) int64 {
	var mask int64
	for _, n := range names {
		if bit, ok := flagBit(n); ok {
			mask |= bit
		}
	}
	switch mode {
	case '+':
		return base | mask
	case '-':
		return base &^ mask
	default: // replace
		return mask
	}
}
