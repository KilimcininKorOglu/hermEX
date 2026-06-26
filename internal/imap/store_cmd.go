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
	if len(args) < 2 {
		c.bad(tag, "STORE requires a sequence set and flags")
		return
	}
	setText, _ := args[0].str()
	set, err := parseSeqSet(setText)
	if err != nil {
		c.bad(tag, "invalid sequence set")
		return
	}
	// An optional (UNCHANGEDSINCE n) modifier (RFC 7162) precedes the item.
	rest, unchangedSince, condUsed, ok := parseUnchangedSince(args[1:])
	if !ok {
		c.bad(tag, "invalid STORE modifier")
		return
	}
	if condUsed {
		c.condstore = true
	}
	if len(rest) < 2 {
		c.bad(tag, "STORE requires an item and flags")
		return
	}
	itemText, _ := rest[0].str()
	mode, silent, ok := parseStoreItem(itemText)
	if !ok {
		c.bad(tag, "invalid STORE item")
		return
	}
	names := flagValue(rest[1:])

	var preModseqs map[uint32]uint64
	if condUsed {
		preModseqs = c.modseqMap() // pre-store: the UNCHANGEDSINCE comparison basis
	}

	max := c.sel.maxSeq()
	if byUID {
		max = c.sel.maxUID()
	}
	var modified []uint32 // rejected by UNCHANGEDSINCE (their modseq moved on)
	var reported []int    // message indices to report back via FETCH
	for i := range c.sel.msgs {
		seq := uint32(i + 1)
		key := seq
		if byUID {
			key = c.sel.msgs[i].UID
		}
		if !set.contains(key, max) {
			continue
		}
		if condUsed && preModseqs[c.sel.msgs[i].UID] > unchangedSince {
			modified = append(modified, c.sel.msgs[i].UID)
			continue // changed since the client's modseq; do not touch it
		}
		newFlags := applyFlagNames(c.sel.msgs[i].Flags, mode, names)
		if newFlags != c.sel.msgs[i].Flags {
			if err := c.curStore().SetMessageFlags(c.sel.id, c.sel.msgs[i].UID, newFlags); err != nil {
				continue
			}
			c.sel.msgs[i].Flags = newFlags
		}
		// .SILENT suppresses the FETCH, except a conditional STORE still reports the
		// new MODSEQ so the client can track it (RFC 7162).
		if !silent || condUsed {
			reported = append(reported, i)
		}
	}

	// The new modseq is read fresh after the modifications, never the pre-store map.
	var postModseqs map[uint32]uint64
	if c.condstore {
		postModseqs = c.modseqMap()
	}
	for _, i := range reported {
		c.untagged("%d FETCH (%s)", uint32(i+1), storeFetchFields(c.sel.msgs[i], byUID, c.condstore, postModseqs))
	}

	verb := "STORE"
	if byUID {
		verb = "UID STORE"
	}
	if len(modified) > 0 {
		c.ok(tag, fmt.Sprintf("[MODIFIED %s] %s completed", uidList(modified), verb))
		return
	}
	c.ok(tag, verb+" completed")
}

// storeFetchFields builds the FETCH data for a STORE reply: FLAGS, the UID for a
// UID STORE, and MODSEQ once CONDSTORE is enabled.
func storeFetchFields(m objectstore.MessageInfo, byUID, condstore bool, modseqs map[uint32]uint64) string {
	parts := []string{fmt.Sprintf("FLAGS (%s)", formatFlags(m.Flags, false))}
	if byUID {
		parts = append(parts, fmt.Sprintf("UID %d", m.UID))
	}
	if condstore {
		parts = append(parts, fmt.Sprintf("MODSEQ (%d)", modseqs[m.UID]))
	}
	return strings.Join(parts, " ")
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
	var vanished []uint32
	seq := uint32(1)
	for _, m := range c.sel.msgs {
		if m.Flags&objectstore.FlagDeleted != 0 {
			if err := c.curStore().SoftDeleteMessage(c.sel.id, m.UID); err == nil {
				vanished = append(vanished, m.UID)
				if emit && !c.qresync {
					c.untagged("%d EXPUNGE", seq) // removed; the next message takes this slot
				}
			}
			continue
		}
		survivors = append(survivors, m)
		seq++
	}
	c.sel.msgs = survivors
	// QRESYNC (RFC 7162): one VANISHED line carries the expunged UIDs in place of the
	// per-message EXPUNGE responses.
	if emit && c.qresync && len(vanished) > 0 {
		c.untagged("VANISHED %s", esearchSet(vanished))
	}
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
	var vanished []uint32
	seq := uint32(1)
	for _, m := range c.sel.msgs {
		if m.Flags&objectstore.FlagDeleted != 0 && set.contains(m.UID, max) {
			if err := c.curStore().SoftDeleteMessage(c.sel.id, m.UID); err == nil {
				vanished = append(vanished, m.UID)
				if !c.qresync {
					c.untagged("%d EXPUNGE", seq)
				}
			}
			continue
		}
		survivors = append(survivors, m)
		seq++
	}
	c.sel.msgs = survivors
	if c.qresync && len(vanished) > 0 {
		c.untagged("VANISHED %s", esearchSet(vanished))
	}
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
	destStore, destFID, ok, errText := c.resolveAppendDest(mailbox)
	if !ok {
		c.no(tag, errText)
		return
	}

	// MULTIAPPEND (RFC 3502): one APPEND may carry several (flags? date? message)
	// groups. Append each; if any fails, roll back the ones already stored so the
	// command is atomic.
	rest := args[1:]
	var uids []uint32
	for len(rest) > 0 {
		flags, r2 := appendFlags(rest)
		date, r3 := appendDate(r2)
		if len(r3) < 1 || !r3[0].literal {
			c.bad(tag, "APPEND requires a message literal")
			return
		}
		info, err := destStore.AppendMessage(destFID, []byte(r3[0].val), date, flags)
		if err != nil {
			for _, u := range uids {
				destStore.SoftDeleteMessage(destFID, u)
			}
			c.no(tag, "APPEND failed")
			return
		}
		uids = append(uids, info.UID)
		rest = r3[1:]
	}
	if len(uids) == 0 {
		c.bad(tag, "APPEND requires a message literal")
		return
	}

	// Surface the new count only when the destination IS the selected folder. Folder
	// ids are not unique across the own and public stores, so the selection's store
	// and id must both match — comparing ids alone would falsely fire across stores.
	if c.state == stateSelected && c.curStore() == destStore && c.sel.id == destFID {
		c.poll()
	}
	// UIDPLUS (RFC 4315): report the assigned UID(s) so the client need not search
	// for the messages it just uploaded.
	uidv, _ := destStore.UIDValidity(destFID)
	c.ok(tag, fmt.Sprintf("[APPENDUID %d %s] APPEND completed", uidv, uidList(uids)))
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
