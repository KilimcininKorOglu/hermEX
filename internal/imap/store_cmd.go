package imap

import (
	"fmt"
	"strings"
	"time"

	"hermex/internal/objectstore"
)

// appendDateLayout is the IMAP date-time used by APPEND and INTERNALDATE.
const appendDateLayout = "02-Jan-2006 15:04:05 -0700"

// cmdStore handles STORE and (byUID) UID STORE: it updates message flags and,
// unless .SILENT, reports the new flags as an untagged FETCH.
func (c *conn) cmdStore(tag string, args []token, byUID bool) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	if c.readOnly {
		c.no(tag, "mailbox is read-only")
		return
	}
	if len(args) < 3 {
		c.bad(tag, "STORE requires a sequence set, item, and flags")
		return
	}
	setText, _ := args[0].str()
	set, err := parseSeqSet(setText)
	if err != nil {
		c.bad(tag, "invalid sequence set")
		return
	}
	itemText, _ := args[1].str()
	mode, silent, ok := parseStoreItem(itemText)
	if !ok {
		c.bad(tag, "invalid STORE item")
		return
	}
	names := flagValue(args[2:])

	max := c.sel.maxSeq()
	if byUID {
		max = c.sel.maxUID()
	}
	for i := range c.sel.msgs {
		seq := uint32(i + 1)
		key := seq
		if byUID {
			key = c.sel.msgs[i].UID
		}
		if !set.contains(key, max) {
			continue
		}
		newFlags := applyFlagNames(c.sel.msgs[i].Flags, mode, names)
		if newFlags != c.sel.msgs[i].Flags {
			if err := c.curStore().SetMessageFlags(c.sel.id, c.sel.msgs[i].UID, newFlags); err != nil {
				continue
			}
			c.sel.msgs[i].Flags = newFlags
		}
		if !silent {
			if byUID {
				c.untagged("%d FETCH (FLAGS (%s) UID %d)", seq, formatFlags(newFlags, false), c.sel.msgs[i].UID)
			} else {
				c.untagged("%d FETCH (FLAGS (%s))", seq, formatFlags(newFlags, false))
			}
		}
	}
	verb := "STORE"
	if byUID {
		verb = "UID STORE"
	}
	c.ok(tag, verb+" completed")
}

// parseStoreItem decodes a STORE item like "+FLAGS.SILENT" into a fold mode
// ('+', '-', or ' ' for replace) and whether the .SILENT suffix was present.
func parseStoreItem(item string) (mode byte, silent bool, ok bool) {
	item = strings.ToUpper(item)
	mode = ' '
	if strings.HasPrefix(item, "+") {
		mode, item = '+', item[1:]
	} else if strings.HasPrefix(item, "-") {
		mode, item = '-', item[1:]
	}
	if s, found := strings.CutSuffix(item, ".SILENT"); found {
		silent, item = true, s
	}
	return mode, silent, item == "FLAGS"
}

// flagValue extracts the flag names from a STORE value: a parenthesized list or
// a bare sequence of flag atoms.
func flagValue(args []token) []string {
	if len(args) > 0 && args[0].kind == tLParen {
		return parenAtoms(args)
	}
	var names []string
	for _, t := range args {
		if s, ok := t.str(); ok {
			names = append(names, s)
		}
	}
	return names
}

// cmdExpunge handles EXPUNGE: it permanently removes \Deleted messages and
// reports each removal as an untagged EXPUNGE.
func (c *conn) cmdExpunge(tag string) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	if c.readOnly {
		c.no(tag, "mailbox is read-only")
		return
	}
	c.doExpunge(true)
	c.ok(tag, "EXPUNGE completed")
}

// doExpunge soft-deletes every \Deleted message into the Recoverable Items
// dumpster and rebuilds the snapshot. The messages leave the mailbox but stay
// recoverable until retention. When emit is true it sends an untagged EXPUNGE per
// removed message, numbered against the shrinking mailbox (RFC 3501 §7.4.1).
func (c *conn) doExpunge(emit bool) {
	var survivors []objectstore.MessageInfo
	seq := uint32(1)
	for _, m := range c.sel.msgs {
		if m.Flags&objectstore.FlagDeleted != 0 {
			if err := c.curStore().SoftDeleteMessage(c.sel.id, m.UID); err == nil && emit {
				c.untagged("%d EXPUNGE", seq) // removed; the next message takes this slot
			}
			continue
		}
		survivors = append(survivors, m)
		seq++
	}
	c.sel.msgs = survivors
	c.flush()
}

// cmdUIDExpunge handles UID EXPUNGE (RFC 4315): it expunges only the \Deleted
// messages whose UID is in the set, leaving other \Deleted messages in place.
func (c *conn) cmdUIDExpunge(tag string, args []token) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	if c.readOnly {
		c.no(tag, "mailbox is read-only")
		return
	}
	if len(args) < 1 {
		c.bad(tag, "UID EXPUNGE requires a sequence set")
		return
	}
	setText, _ := args[0].str()
	set, err := parseSeqSet(setText)
	if err != nil {
		c.bad(tag, "invalid sequence set")
		return
	}
	max := c.sel.maxUID()
	var survivors []objectstore.MessageInfo
	seq := uint32(1)
	for _, m := range c.sel.msgs {
		if m.Flags&objectstore.FlagDeleted != 0 && set.contains(m.UID, max) {
			if err := c.curStore().SoftDeleteMessage(c.sel.id, m.UID); err == nil {
				c.untagged("%d EXPUNGE", seq)
			}
			continue
		}
		survivors = append(survivors, m)
		seq++
	}
	c.sel.msgs = survivors
	c.flush()
	c.ok(tag, "UID EXPUNGE completed")
}

// uidList renders a comma-separated UID set for an APPENDUID/COPYUID response code.
func uidList(ns []uint32) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return strings.Join(parts, ",")
}

// cmdClose handles CLOSE: it silently expunges \Deleted messages and returns to
// the authenticated (no mailbox selected) state.
func (c *conn) cmdClose(tag string) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	if !c.readOnly {
		c.doExpunge(false)
	}
	c.sel = nil
	c.selPublic = false
	c.state = stateAuth
	c.ok(tag, "CLOSE completed")
}

// cmdUnselect handles UNSELECT (RFC 3691): it returns to the authenticated state
// WITHOUT expunging \Deleted messages, unlike CLOSE.
func (c *conn) cmdUnselect(tag string) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	c.sel = nil
	c.selPublic = false
	c.state = stateAuth
	c.ok(tag, "UNSELECT completed")
}

// cmdCopy handles COPY and (byUID) UID COPY: it copies the addressed messages
// into another mailbox, preserving their flags and internal dates.
func (c *conn) cmdCopy(tag string, args []token, byUID bool) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	if len(args) < 2 {
		c.bad(tag, "COPY requires a sequence set and a mailbox")
		return
	}
	setText, _ := args[0].str()
	set, err := parseSeqSet(setText)
	if err != nil {
		c.bad(tag, "invalid sequence set")
		return
	}
	dest, _ := args[1].str()
	destStore, destFID, ok, errText := c.resolveAppendDest(dest)
	if !ok {
		c.no(tag, errText)
		return
	}

	max := c.sel.maxSeq()
	if byUID {
		max = c.sel.maxUID()
	}
	src := c.curStore()
	var srcUIDs, dstUIDs []uint32
	for i := range c.sel.msgs {
		key := uint32(i + 1)
		if byUID {
			key = c.sel.msgs[i].UID
		}
		if !set.contains(key, max) {
			continue
		}
		raw, err := src.GetMessageRaw(c.sel.id, c.sel.msgs[i].UID)
		if err != nil {
			c.no(tag, "copy failed")
			return
		}
		info, err := destStore.AppendMessage(destFID, raw, c.sel.msgs[i].InternalDate, c.sel.msgs[i].Flags)
		if err != nil {
			c.no(tag, "copy failed")
			return
		}
		srcUIDs = append(srcUIDs, c.sel.msgs[i].UID)
		dstUIDs = append(dstUIDs, info.UID)
	}
	verb := "COPY"
	if byUID {
		verb = "UID COPY"
	}
	// UIDPLUS (RFC 4315): report the source→destination UID mapping so the client
	// need not re-sync the destination to learn the new UIDs.
	if len(srcUIDs) > 0 {
		uidv, _ := destStore.UIDValidity(destFID)
		c.ok(tag, fmt.Sprintf("[COPYUID %d %s %s] %s completed", uidv, uidList(srcUIDs), uidList(dstUIDs), verb))
		return
	}
	c.ok(tag, verb+" completed")
}

// cmdMove handles MOVE and (byUID) UID MOVE (RFC 6851): it copies the addressed
// messages into another mailbox and removes them from the source. It reuses the
// COPY path (so a cross-store move to/from a public folder works) plus the
// soft-delete dumpster. Per RFC 6851 the UID mapping is reported in an untagged OK
// [COPYUID ...] before an untagged EXPUNGE for each removed source message.
func (c *conn) cmdMove(tag string, args []token, byUID bool) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	if c.readOnly {
		c.no(tag, "mailbox is read-only")
		return
	}
	if len(args) < 2 {
		c.bad(tag, "MOVE requires a sequence set and a mailbox")
		return
	}
	setText, _ := args[0].str()
	set, err := parseSeqSet(setText)
	if err != nil {
		c.bad(tag, "invalid sequence set")
		return
	}
	dest, _ := args[1].str()
	destStore, destFID, ok, errText := c.resolveAppendDest(dest)
	if !ok {
		c.no(tag, errText)
		return
	}

	verb := "MOVE"
	if byUID {
		verb = "UID MOVE"
	}
	max := c.sel.maxSeq()
	if byUID {
		max = c.sel.maxUID()
	}
	src := c.curStore()

	// First pass: copy each matching message to the destination.
	var srcUIDs, dstUIDs []uint32
	moved := make(map[uint32]bool)
	for i := range c.sel.msgs {
		key := uint32(i + 1)
		if byUID {
			key = c.sel.msgs[i].UID
		}
		if !set.contains(key, max) {
			continue
		}
		raw, err := src.GetMessageRaw(c.sel.id, c.sel.msgs[i].UID)
		if err != nil {
			c.no(tag, "move failed")
			return
		}
		info, err := destStore.AppendMessage(destFID, raw, c.sel.msgs[i].InternalDate, c.sel.msgs[i].Flags)
		if err != nil {
			c.no(tag, "move failed")
			return
		}
		srcUIDs = append(srcUIDs, c.sel.msgs[i].UID)
		dstUIDs = append(dstUIDs, info.UID)
		moved[c.sel.msgs[i].UID] = true
	}
	if len(srcUIDs) == 0 {
		c.ok(tag, verb+" completed")
		return
	}

	uidv, _ := destStore.UIDValidity(destFID)
	c.untagged("OK [COPYUID %d %s %s]", uidv, uidList(srcUIDs), uidList(dstUIDs))

	// Second pass: soft-delete each moved source message, emitting EXPUNGE against
	// the shrinking mailbox.
	var survivors []objectstore.MessageInfo
	seq := uint32(1)
	for _, m := range c.sel.msgs {
		if moved[m.UID] {
			if err := src.SoftDeleteMessage(c.sel.id, m.UID); err == nil {
				c.untagged("%d EXPUNGE", seq)
			}
			continue
		}
		survivors = append(survivors, m)
		seq++
	}
	c.sel.msgs = survivors
	c.flush()
	c.ok(tag, verb+" completed")
}

// cmdAppend handles APPEND: it stores a supplied message into a mailbox with
// optional flags and internal date.
func (c *conn) cmdAppend(tag string, args []token) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	if len(args) < 2 {
		c.bad(tag, "APPEND requires a mailbox and a message")
		return
	}
	mailbox, _ := args[0].str()
	rest := args[1:]

	flags, rest := appendFlags(rest)
	date, rest := appendDate(rest)
	if len(rest) < 1 {
		c.bad(tag, "APPEND requires a message literal")
		return
	}
	msg, ok := rest[0].str()
	if !ok {
		c.bad(tag, "APPEND message must be a literal")
		return
	}

	destStore, destFID, ok, errText := c.resolveAppendDest(mailbox)
	if !ok {
		c.no(tag, errText)
		return
	}
	info, err := destStore.AppendMessage(destFID, []byte(msg), date, flags)
	if err != nil {
		c.no(tag, "APPEND failed")
		return
	}
	// Surface the new count only when the destination IS the selected folder. Folder
	// ids are not unique across the own and public stores, so the selection's store
	// and id must both match — comparing ids alone would falsely fire across stores.
	if c.state == stateSelected && c.curStore() == destStore && c.sel.id == destFID {
		c.poll()
	}
	// UIDPLUS (RFC 4315): report the assigned UID so the client need not search for
	// the message it just uploaded.
	uidv, _ := destStore.UIDValidity(destFID)
	c.ok(tag, fmt.Sprintf("[APPENDUID %d %d] APPEND completed", uidv, info.UID))
}

// appendFlags consumes an optional leading parenthesized flag list, returning
// the folded flag bits and the remaining tokens.
func appendFlags(args []token) (int64, []token) {
	if len(args) == 0 || args[0].kind != tLParen {
		return 0, args
	}
	names := parenAtoms(args)
	// Skip past the closing ')'.
	depth := 0
	for i, t := range args {
		switch t.kind {
		case tLParen:
			depth++
		case tRParen:
			depth--
			if depth == 0 {
				return applyFlagNames(0, ' ', names), args[i+1:]
			}
		}
	}
	return applyFlagNames(0, ' ', names), nil
}

// appendDate consumes an optional date-time string, returning it (or the
// current time when absent) and the remaining tokens.
func appendDate(args []token) (time.Time, []token) {
	if len(args) >= 2 { // a date-time precedes the message literal
		if s, ok := args[0].str(); ok && !args[0].literal {
			if t, err := time.Parse(appendDateLayout, s); err == nil {
				return t, args[1:]
			}
		}
	}
	return time.Now().UTC(), args
}

// ids renders a space-separated list of numbers for a SEARCH response.
func ids(ns []uint32) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return strings.Join(parts, " ")
}
