package imap

import (
	"strconv"
	"strings"
)

// CONDSTORE/QRESYNC (RFC 7162). MODSEQ is the objectstore's IMAP-local per-message
// modification sequence (one space per folder). A session reports it once enabled,
// which happens via ENABLE, a SELECT (CONDSTORE), or any CHANGEDSINCE/UNCHANGEDSINCE
// reference.

// cmdEnable handles ENABLE (RFC 5161): it turns on the named per-session
// extensions. Only CONDSTORE/QRESYNC carry session state here.
func (c *conn) cmdEnable(tag string, args []token) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	var enabled []string
	for _, a := range args {
		switch strings.ToUpper(a.val) {
		case "CONDSTORE", "QRESYNC":
			c.condstore = true
			enabled = append(enabled, strings.ToUpper(a.val))
		}
	}
	if len(enabled) > 0 {
		c.untagged("ENABLED %s", strings.Join(enabled, " "))
	}
	c.ok(tag, "ENABLE completed")
}

// modseqMap reads the selected folder's per-UID modification sequences fresh, so a
// STORE reports the new modseq it just assigned (never a cached SELECT snapshot).
func (c *conn) modseqMap() map[uint32]uint64 {
	m, _ := c.curStore().MessageModSeqs(c.sel.id)
	return m
}

// highestModSeq returns the selected folder's HIGHESTMODSEQ.
func (c *conn) highestModSeq() uint64 {
	hi, _ := c.curStore().FolderHighestModSeq(c.sel.id)
	return hi
}

// selectEnablesCondstore reports whether a SELECT/EXAMINE parameter list carries
// (CONDSTORE) or (QRESYNC ...), which switch the session into CONDSTORE mode.
func selectEnablesCondstore(args []token) bool {
	for _, a := range args {
		if a.kind == tAtom && (strings.EqualFold(a.val, "CONDSTORE") || strings.EqualFold(a.val, "QRESYNC")) {
			return true
		}
	}
	return false
}

// splitFetchModifiers strips a trailing (CHANGEDSINCE n [VANISHED]) modifier group
// (RFC 7162) from the FETCH item tokens, returning the remaining item tokens and
// the CHANGEDSINCE value (0 = none). ok=false marks a malformed modifier.
func splitFetchModifiers(args []token) (items []token, changedSince uint64, ok bool) {
	depth, lastOpen := 0, -1
	for i, t := range args {
		switch t.kind {
		case tLParen:
			if depth == 0 {
				lastOpen = i
			}
			depth++
		case tRParen:
			depth--
		}
	}
	if lastOpen < 0 {
		return args, 0, true
	}
	inner := args[lastOpen+1:]
	if len(inner) == 0 || inner[0].kind != tAtom || !strings.EqualFold(inner[0].val, "CHANGEDSINCE") {
		return args, 0, true
	}
	if len(inner) < 3 {
		return nil, 0, false
	}
	n, err := strconv.ParseUint(inner[1].val, 10, 64)
	if err != nil {
		return nil, 0, false
	}
	return args[:lastOpen], n, true
}

// parseUnchangedSince extracts the (UNCHANGEDSINCE n) STORE modifier (RFC 7162) if
// it leads the STORE arguments after the sequence set, returning the modseq, the
// remaining tokens, and ok=false on a malformed modifier.
func parseUnchangedSince(args []token) (rest []token, modseq uint64, present bool, ok bool) {
	if len(args) < 1 || args[0].kind != tLParen {
		return args, 0, false, true
	}
	// args = [ ( UNCHANGEDSINCE n ) flag-args... ]
	if len(args) < 4 || args[1].kind != tAtom || !strings.EqualFold(args[1].val, "UNCHANGEDSINCE") {
		return args, 0, false, true
	}
	n, err := strconv.ParseUint(args[2].val, 10, 64)
	if err != nil || args[3].kind != tRParen {
		return nil, 0, false, false
	}
	return args[4:], n, true, true
}
