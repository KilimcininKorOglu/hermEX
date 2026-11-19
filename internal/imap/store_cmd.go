package imap

import (
	"fmt"
	"strings"
	"time"

	"hermex/internal/logging"
	"hermex/internal/mta"
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
	req, problem := c.parseStoreRequest(args[1:])
	if problem != "" {
		c.bad(tag, problem)
		return
	}
	modified, reported := c.applyStore(set, byUID, req)

	// The new modseq is read fresh after the modifications, never the pre-store map.
	var postModseqs map[uint32]uint64
	if c.condstore {
		postModseqs = c.modseqMap()
	}
	for _, i := range reported {
		// #nosec G115 -- an IMAP sequence number, an index into the selected mailbox's in-memory message list
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

// parseStoreRequest reads the part of a STORE after its sequence set: an optional
// (UNCHANGEDSINCE n) modifier (RFC 7162), the item, and the flag list. A non-empty
// problem is the protocol error to report.
func (c *conn) parseStoreRequest(args []token) (storeRequest, string) {
	rest, unchangedSince, condUsed, ok := parseUnchangedSince(args)
	if !ok {
		return storeRequest{}, "invalid STORE modifier"
	}
	if condUsed {
		c.condstore = true
	}
	if len(rest) < 2 {
		return storeRequest{}, "STORE requires an item and flags"
	}
	itemText, _ := rest[0].str()
	mode, silent, ok := parseStoreItem(itemText)
	if !ok {
		return storeRequest{}, "invalid STORE item"
	}
	return storeRequest{
		mode:           mode,
		silent:         silent,
		names:          flagValue(rest[1:]),
		condUsed:       condUsed,
		unchangedSince: unchangedSince,
	}, ""
}

// storeRequest is one parsed STORE: which flags to apply, how, whether the client
// suppressed the FETCH replies, and the CONDSTORE guard it set.
type storeRequest struct {
	mode           byte
	silent         bool
	names          []string
	condUsed       bool
	unchangedSince uint64
}

// applyStore writes the requested flag change to every message in the set. It
// returns the UIDs UNCHANGEDSINCE rejected (their modseq had moved on) and the
// message indices to report back via FETCH.
func (c *conn) applyStore(set seqSet, byUID bool, req storeRequest) (modified []uint32, reported []int) {
	var preModseqs map[uint32]uint64
	if req.condUsed {
		preModseqs = c.modseqMap() // pre-store: the UNCHANGEDSINCE comparison basis
	}
	max := c.sel.maxSeq()
	if byUID {
		max = c.sel.maxUID()
	}
	for i := range c.sel.msgs {
		// #nosec G115 -- an IMAP sequence number, an index into the selected mailbox's in-memory message list
		key := uint32(i + 1)
		if byUID {
			key = c.sel.msgs[i].UID
		}
		if !set.contains(key, max) {
			continue
		}
		if req.condUsed && preModseqs[c.sel.msgs[i].UID] > req.unchangedSince {
			modified = append(modified, c.sel.msgs[i].UID)
			continue // changed since the client's modseq; do not touch it
		}
		c.storeFlags(i, req)
		// .SILENT suppresses the FETCH, except a conditional STORE still reports the
		// new MODSEQ so the client can track it (RFC 7162).
		if !req.silent || req.condUsed {
			reported = append(reported, i)
		}
	}
	return modified, reported
}

// storeFlags applies the flag change to one message, updating the session copy
// only once the store took the write.
func (c *conn) storeFlags(i int, req storeRequest) {
	newFlags := applyFlagNames(c.sel.msgs[i].Flags, req.mode, req.names)
	if newFlags == c.sel.msgs[i].Flags {
		return
	}
	if err := c.curStore().SetMessageFlags(c.sel.id, c.sel.msgs[i].UID, newFlags); err != nil {
		return
	}
	c.sel.msgs[i].Flags = newFlags
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
	c.answerExpunge(tag, "EXPUNGE", c.doExpunge(true))
}

// isDeleted reports whether a message carries \Deleted, the mark EXPUNGE acts on.
func isDeleted(m objectstore.MessageInfo) bool { return m.Flags&objectstore.FlagDeleted != 0 }

// answerExpunge ends an expunging command: OK when every \Deleted message went,
// NO when the store kept any (those keep their slot and get no EXPUNGE).
func (c *conn) answerExpunge(tag, cmd string, failed int) {
	if failed > 0 {
		c.no(tag, fmt.Sprintf("%s: %d message(s) could not be expunged", cmd, failed))
		return
	}
	c.ok(tag, cmd+" completed")
}

// doExpunge soft-deletes every \Deleted message into the Recoverable Items
// dumpster and rebuilds the snapshot, reporting how many the store refused. When
// emit is true it announces each removal (see expungeWhere).
func (c *conn) doExpunge(emit bool) int {
	vanished, failed := c.expungeWhere(isDeleted, emit)
	// QRESYNC (RFC 7162): one VANISHED line carries the expunged UIDs in place of the
	// per-message EXPUNGE responses.
	if emit && c.qresync && len(vanished) > 0 {
		c.untagged("VANISHED %s", esearchSet(vanished))
	}
	c.flush()
	return failed
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
	vanished, failed := c.expungeWhere(func(m objectstore.MessageInfo) bool {
		return isDeleted(m) && set.contains(m.UID, max)
	}, true)
	if c.qresync && len(vanished) > 0 {
		c.untagged("VANISHED %s", esearchSet(vanished))
	}
	c.flush()
	c.answerExpunge(tag, "UID EXPUNGE", failed)
}

// expungeWhere soft-deletes every message match selects and returns the UIDs that went plus how many the store refused. When
// emit is set, each removal gets an EXPUNGE numbered against the shrinking mailbox
// (RFC 3501 7.4.1); QRESYNC clients get one VANISHED from the caller instead. A
// message the store refused stays in the snapshot and keeps its number, so the
// client and the server keep counting the same mailbox, and the failure is logged.
func (c *conn) expungeWhere(match func(objectstore.MessageInfo) bool, emit bool) (vanished []uint32, failed int) {
	var survivors []objectstore.MessageInfo
	seq := uint32(1)
	for _, m := range c.sel.msgs {
		if !match(m) {
			survivors = append(survivors, m)
			seq++
			continue
		}
		if err := c.curStore().SoftDeleteMessage(c.sel.id, m.UID); err != nil {
			c.event(logging.LevelError, "expunge.fail", logging.Fields{"folder": c.sel.id, "uid": m.UID, "error": err.Error()})
			failed++
			survivors = append(survivors, m)
			seq++
			continue
		}
		vanished = append(vanished, m.UID)
		if emit && !c.qresync {
			c.untagged("%d EXPUNGE", seq) // removed; the next message takes this slot
		}
	}
	c.sel.msgs = survivors
	return vanished, failed
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

// refuseOverQuota answers tag with NO [OVERQUOTA] (RFC 9208 4.3) and reports true
// when st, the store a command would add messages to, is past its receive quota:
// once a mailbox is full nothing new may be put in it, from delivery or from a
// client. A store that cannot report its usage refuses the command as failed.
func (c *conn) refuseOverQuota(tag string, st *objectstore.Store) bool {
	over, err := st.OverReceiveQuota()
	switch {
	case err != nil:
		c.no(tag, "cannot read the mailbox quota")
		return true
	case over:
		c.no(tag, "[OVERQUOTA] mailbox is over its quota")
		return true
	}
	return false
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
	if c.refuseOverQuota(tag, destStore) {
		return
	}

	srcUIDs, dstUIDs, ok := c.copyToDest(set, byUID, destStore, destFID)
	if !ok {
		c.no(tag, "copy failed")
		return
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
	set, destStore, destFID, ok := c.moveTarget(tag, args)
	if !ok {
		return
	}

	verb := "MOVE"
	if byUID {
		verb = "UID MOVE"
	}
	// First pass: copy each matching message to the destination.
	srcUIDs, dstUIDs, ok := c.copyToDest(set, byUID, destStore, destFID)
	if !ok {
		c.no(tag, "move failed")
		return
	}
	if len(srcUIDs) == 0 {
		c.ok(tag, verb+" completed")
		return
	}

	uidv, _ := destStore.UIDValidity(destFID)
	c.untagged("OK [COPYUID %d %s %s]", uidv, uidList(srcUIDs), uidList(dstUIDs))
	failed := c.expungeMoved(srcUIDs)
	c.flush()
	c.answerExpunge(tag, verb, failed)
}

// moveTarget parses a MOVE's sequence set and destination and checks the
// destination may take the messages. When ok is false the reply is already
// written.
func (c *conn) moveTarget(tag string, args []token) (seqSet, *objectstore.Store, int64, bool) {
	if len(args) < 2 {
		c.bad(tag, "MOVE requires a sequence set and a mailbox")
		return seqSet{}, nil, 0, false
	}
	setText, _ := args[0].str()
	set, err := parseSeqSet(setText)
	if err != nil {
		c.bad(tag, "invalid sequence set")
		return seqSet{}, nil, 0, false
	}
	dest, _ := args[1].str()
	destStore, destFID, ok, errText := c.resolveAppendDest(dest)
	if !ok {
		c.no(tag, errText)
		return seqSet{}, nil, 0, false
	}
	// A move inside one store adds nothing to it, so a full mailbox can still file
	// its own mail (to Deleted Items, say); only another store would grow.
	if destStore != c.curStore() && c.refuseOverQuota(tag, destStore) {
		return seqSet{}, nil, 0, false
	}
	return set, destStore, destFID, true
}

// copyToDest copies every message in the set into the destination folder,
// returning the source and destination UIDs in step. ok is false when any copy
// failed, and the caller then reports the whole command failed.
func (c *conn) copyToDest(set seqSet, byUID bool, destStore *objectstore.Store, destFID int64) (srcUIDs, dstUIDs []uint32, ok bool) {
	max := c.sel.maxSeq()
	if byUID {
		max = c.sel.maxUID()
	}
	src := c.curStore()
	for i := range c.sel.msgs {
		// #nosec G115 -- an IMAP sequence number, an index into the selected mailbox's in-memory message list
		key := uint32(i + 1)
		if byUID {
			key = c.sel.msgs[i].UID
		}
		if !set.contains(key, max) {
			continue
		}
		raw, err := src.GetMessageRaw(c.sel.id, c.sel.msgs[i].UID)
		if err != nil {
			c.rollbackAppend(destStore, destFID, dstUIDs) // RFC 3501 6.4.7: restore the destination
			return nil, nil, false
		}
		info, err := destStore.AppendMessage(destFID, raw, c.sel.msgs[i].InternalDate, c.sel.msgs[i].Flags)
		if err != nil {
			c.rollbackAppend(destStore, destFID, dstUIDs)
			return nil, nil, false
		}
		srcUIDs = append(srcUIDs, c.sel.msgs[i].UID)
		dstUIDs = append(dstUIDs, info.UID)
	}
	return srcUIDs, dstUIDs, true
}

// expungeMoved soft-deletes each moved source message through expungeWhere, so a
// message the store refused keeps its slot, and returns how many were refused.
func (c *conn) expungeMoved(srcUIDs []uint32) int {
	moved := make(map[uint32]bool, len(srcUIDs))
	for _, uid := range srcUIDs {
		moved[uid] = true
	}
	vanished, failed := c.expungeWhere(func(m objectstore.MessageInfo) bool { return moved[m.UID] }, true)
	if c.qresync && len(vanished) > 0 {
		c.untagged("VANISHED %s", esearchSet(vanished)) // RFC 7162 3.2.10, in place of EXPUNGE
	}
	return failed
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
	if c.refuseOverQuota(tag, destStore) {
		return
	}

	// MULTIAPPEND (RFC 3502): one APPEND may carry several (flags? date? message)
	// groups. Append each; if any fails, roll back the ones already stored so the
	// command is atomic.
	uids, failure, malformed := c.appendGroups(args[1:], destStore, destFID)
	switch {
	case malformed:
		c.bad(tag, failure)
		return
	case failure != "":
		c.no(tag, failure)
		return
	}

	// Surface the new count only when the destination IS the selected folder. Folder
	// ids are not unique across the own and public stores, so the selection's store
	// and id must both match, comparing ids alone would falsely fire across stores.
	if c.state == stateSelected && c.curStore() == destStore && c.sel.id == destFID {
		c.poll()
	}
	// UIDPLUS (RFC 4315): report the assigned UID(s) so the client need not search
	// for the messages it just uploaded.
	uidv, _ := destStore.UIDValidity(destFID)
	c.ok(tag, fmt.Sprintf("[APPENDUID %d %s] APPEND completed", uidv, uidList(uids)))
}

// appendGroups stores every (flags? date? message) group a MULTIAPPEND (RFC 3502)
// carries. If any group fails, the ones already stored are rolled back so the
// command is atomic. A non-empty failure is the text to report; malformed marks
// it a protocol error rather than a refusal.
func (c *conn) appendGroups(rest []token, destStore *objectstore.Store, destFID int64) (uids []uint32, failure string, malformed bool) {
	for len(rest) > 0 {
		flags, r2 := appendFlags(rest)
		date, r3 := appendDate(r2)
		if len(r3) < 1 || !r3[0].literal {
			c.rollbackAppend(destStore, destFID, uids)
			return nil, "APPEND requires a message literal", true
		}
		// An APPEND literal is client-supplied content that never passes through
		// delivery, so this is the only point it can be scanned. A hit is
		// quarantined and the whole command fails, rolling back anything a
		// MULTIAPPEND already stored.
		if mta.ScanStored(c.srv.Accounts, c.user, "", []byte(r3[0].val), time.Now()) {
			c.rollbackAppend(destStore, destFID, uids)
			return nil, "APPEND rejected: a virus was detected", false
		}
		info, err := destStore.AppendMessage(destFID, []byte(r3[0].val), date, flags)
		if err != nil {
			c.rollbackAppend(destStore, destFID, uids)
			return nil, "APPEND failed", false
		}
		uids = append(uids, info.UID)
		rest = r3[1:]
	}
	if len(uids) == 0 {
		return nil, "APPEND requires a message literal", true
	}
	return uids, "", false
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

// rollbackAppend removes messages a MULTIAPPEND already stored, so a command that
// fails partway leaves the mailbox as it was. A failed rollback leaves the mailbox
// holding messages the client was told did not arrive, so it is recorded.
func (c *conn) rollbackAppend(destStore *objectstore.Store, destFID int64, uids []uint32) {
	for _, u := range uids {
		if rerr := destStore.SoftDeleteMessage(destFID, u); rerr != nil {
			c.event(logging.LevelError, "append.rollback.fail", logging.Fields{
				"folder": destFID,
				"uid":    u,
				"err":    rerr.Error(),
			})
		}
	}
}
