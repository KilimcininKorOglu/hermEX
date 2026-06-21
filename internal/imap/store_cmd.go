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

// doExpunge deletes every \Deleted message from the store and rebuilds the
// snapshot. When emit is true it sends an untagged EXPUNGE per removed message,
// numbered against the shrinking mailbox (RFC 3501 §7.4.1).
func (c *conn) doExpunge(emit bool) {
	var survivors []objectstore.MessageInfo
	seq := uint32(1)
	for _, m := range c.sel.msgs {
		if m.Flags&objectstore.FlagDeleted != 0 {
			if err := c.curStore().DeleteMessage(c.sel.id, m.UID); err == nil && emit {
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
		if _, err := destStore.AppendMessage(destFID, raw, c.sel.msgs[i].InternalDate, c.sel.msgs[i].Flags); err != nil {
			c.no(tag, "copy failed")
			return
		}
	}
	verb := "COPY"
	if byUID {
		verb = "UID COPY"
	}
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
	if _, err := destStore.AppendMessage(destFID, []byte(msg), date, flags); err != nil {
		c.no(tag, "APPEND failed")
		return
	}
	// Surface the new count only when the destination IS the selected folder. Folder
	// ids are not unique across the own and public stores, so the selection's store
	// and id must both match — comparing ids alone would falsely fire across stores.
	if c.state == stateSelected && c.curStore() == destStore && c.sel.id == destFID {
		c.poll()
	}
	c.ok(tag, "APPEND completed")
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
