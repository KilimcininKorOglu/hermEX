package imap

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"hermex/internal/logging"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
)

// fetchItem is one parsed FETCH data item to produce for each message.
type fetchItem struct {
	kind    string       // FLAGS, UID, ENVELOPE, INTERNALDATE, RFC822.SIZE, BODY, BODYSTRUCTURE, SECTION
	peek    bool         // SECTION: BODY.PEEK (does not set \Seen)
	section mime.Section // SECTION: which part to extract
	name    string       // SECTION: echoed item name, e.g. BODY[HEADER] or RFC822.TEXT
	partial *[2]int      // SECTION: optional <start.count>
}

// cmdFetch handles FETCH and (when byUID) UID FETCH.
func (c *conn) cmdFetch(tag string, args []token, byUID bool) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	if len(args) < 2 {
		c.bad(tag, "FETCH requires a sequence set and items")
		return
	}
	setText, _ := args[0].str()
	set, err := parseSeqSet(setText)
	if err != nil {
		c.bad(tag, "invalid sequence set")
		return
	}
	itemArgs, changedSince, ok := splitFetchModifiers(args[1:])
	if !ok {
		c.bad(tag, "invalid FETCH modifier")
		return
	}
	items, err := parseFetchItems(itemArgs)
	if err != nil {
		c.bad(tag, "invalid FETCH items")
		return
	}
	items = c.completeFetchItems(items, byUID, changedSince)
	var modseqs map[uint32]uint64
	if changedSince > 0 || hasKind(items, "MODSEQ") {
		modseqs = c.modseqMap()
	}
	c.writeFetches(set, byUID, items, modseqs, changedSince)

	verb := "FETCH"
	if byUID {
		verb = "UID FETCH"
	}
	c.ok(tag, verb+" completed")
}

// completeFetchItems adds the items the protocol requires beyond what the client
// listed: a UID FETCH always returns UID, and CHANGEDSINCE (RFC 7162) enables
// CONDSTORE and forces MODSEQ into the response.
func (c *conn) completeFetchItems(items []fetchItem, byUID bool, changedSince uint64) []fetchItem {
	if byUID && !hasKind(items, "UID") {
		items = append(items, fetchItem{kind: "UID"})
	}
	if changedSince > 0 {
		c.condstore = true
		if !hasKind(items, "MODSEQ") {
			items = append(items, fetchItem{kind: "MODSEQ"})
		}
	}
	return items
}

// writeFetches emits one FETCH response per message in the set, skipping the ones
// a CHANGEDSINCE guard says the client already has.
func (c *conn) writeFetches(set seqSet, byUID bool, items []fetchItem, modseqs map[uint32]uint64, changedSince uint64) {
	max := c.sel.maxSeq()
	if byUID {
		max = c.sel.maxUID()
	}
	for i := range c.sel.msgs {
		// #nosec G115 -- an IMAP sequence number, an index into the selected mailbox's in-memory message list
		seq := uint32(i + 1)
		key := seq
		if byUID {
			key = c.sel.msgs[i].UID
		}
		if !set.contains(key, max) {
			continue
		}
		if changedSince > 0 && modseqs[c.sel.msgs[i].UID] <= changedSince {
			continue // unchanged since the client's modseq
		}
		c.writeFetch(seq, i, items, modseqs)
	}
}

// cmdUID dispatches the UID variant of a data command (RFC 3501 §6.4.8), where
// the sequence set names UIDs and the response always carries UID.
func (c *conn) cmdUID(tag string, args []token) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	sub, ok := arg0(args)
	if !ok {
		c.bad(tag, "UID requires a command")
		return
	}
	handler, ok := uidSubcommands[strings.ToUpper(sub)]
	if !ok {
		c.bad(tag, "UID "+sub+" not supported")
		return
	}
	handler(c, tag, args[1:])
}

// uidSubcommands routes the data commands that take a UID form. Each is the same
// handler the non-UID form uses, with the flag that makes its sequence set name
// UIDs; EXPUNGE has no non-UID form and is its own handler.
var uidSubcommands = map[string]func(c *conn, tag string, args []token){
	"FETCH":   func(c *conn, tag string, args []token) { c.cmdFetch(tag, args, true) },
	"STORE":   func(c *conn, tag string, args []token) { c.cmdStore(tag, args, true) },
	"SEARCH":  func(c *conn, tag string, args []token) { c.cmdSearch(tag, args, true) },
	"COPY":    func(c *conn, tag string, args []token) { c.cmdCopy(tag, args, true) },
	"EXPUNGE": (*conn).cmdUIDExpunge,
	"MOVE":    func(c *conn, tag string, args []token) { c.cmdMove(tag, args, true) },
	"SORT":    func(c *conn, tag string, args []token) { c.cmdSort(tag, args, true) },
	"THREAD":  func(c *conn, tag string, args []token) { c.cmdThread(tag, args, true) },
}

func hasKind(items []fetchItem, kind string) bool {
	for _, it := range items {
		if it.kind == kind {
			return true
		}
	}
	return false
}

// writeFetch renders and writes one message's FETCH response. It loads and
// parses the message only when an item actually needs the body or structure,
// and applies the \Seen side effect of a non-peek body fetch.
func (c *conn) writeFetch(seq uint32, idx int, items []fetchItem, modseqs map[uint32]uint64) {
	msg := c.sel.msgs[idx]
	r := &fetchRenderer{c: c, msg: msg}

	var fields []string
	setSeen := false
	for _, it := range items {
		field, ok, reads := r.field(it, modseqs)
		if !ok {
			continue
		}
		fields = append(fields, field)
		setSeen = setSeen || reads
	}

	// A read-only selection (EXAMINE, or a public folder the caller cannot post to)
	// must not implicitly set \Seen, and must never write to the public store.
	if setSeen && !c.readOnly && msg.Flags&objectstore.FlagSeen == 0 {
		if c.markSeen(&c.sel.msgs[idx], c.curStore()) {
			msg.Flags = c.sel.msgs[idx].Flags
			if !hasFlagsField(fields) {
				fields = append(fields, fmt.Sprintf(`FLAGS (%s)`, formatFlags(msg.Flags, false)))
			}
		}
	}

	c.wf("* %d FETCH (%s)\r\n", seq, strings.Join(fields, " "))
	c.flush()
}

// fetchRenderer renders one message's FETCH fields, loading the raw message and
// parsing its structure only when a requested item actually needs them.
type fetchRenderer struct {
	c         *conn
	msg       objectstore.MessageInfo
	raw       []byte
	rawLoaded bool
	structure *mime.Part
}

// loadRaw returns the message's wire form, reading it once.
func (r *fetchRenderer) loadRaw() []byte {
	if !r.rawLoaded {
		r.raw, _ = r.c.curStore().GetMessageRaw(r.c.sel.id, r.msg.UID)
		r.rawLoaded = true
	}
	return r.raw
}

// need returns the message's parsed MIME structure, parsing it once.
func (r *fetchRenderer) need() *mime.Part {
	if r.structure == nil {
		r.structure = mime.ParseStructure(r.loadRaw())
	}
	return r.structure
}

// field renders one requested item. ok is false for an item this server does not
// serve, and reads is true when the item read the body, which implicitly sets
// \Seen unless the client asked with .PEEK.
func (r *fetchRenderer) field(it fetchItem, modseqs map[uint32]uint64) (field string, ok, reads bool) {
	switch it.kind {
	case "SECTION":
		data, found := r.need().Extract(it.section)
		if !found {
			data = []byte{}
		}
		return it.name + " " + literalize(string(applyPartial(data, it.partial))), true, !it.peek
	case "BINARY":
		data, found := extractBinary(r.need(), it.section.Path)
		if !found {
			data = []byte{}
		}
		return it.name + " " + binaryLiteral(applyPartial(data, it.partial)), true, !it.peek
	case "BINARY.SIZE":
		sz := 0
		if data, found := extractBinary(r.need(), it.section.Path); found {
			sz = len(data)
		}
		return fmt.Sprintf("%s %d", it.name, sz), true, false
	}
	field, ok = r.metaField(it, modseqs)
	return field, ok, false
}

// metaField renders the items that describe the message rather than read a part
// of its body.
func (r *fetchRenderer) metaField(it fetchItem, modseqs map[uint32]uint64) (string, bool) {
	switch it.kind {
	case "UID":
		return fmt.Sprintf("UID %d", r.msg.UID), true
	case "MODSEQ":
		return fmt.Sprintf("MODSEQ (%d)", modseqs[r.msg.UID]), true
	case "FLAGS":
		return fmt.Sprintf(`FLAGS (%s)`, formatFlags(r.msg.Flags, false)), true
	case "INTERNALDATE":
		return `INTERNALDATE ` + quoteString(r.msg.InternalDate.Format("02-Jan-2006 15:04:05 -0700")), true
	case "RFC822.SIZE":
		return fmt.Sprintf("RFC822.SIZE %d", r.msg.Size), true
	case "ENVELOPE":
		env, _ := mime.ParseEnvelope(r.loadRaw())
		return "ENVELOPE " + renderEnvelope(env), true
	case "BODY":
		return "BODY " + renderBodyStructure(r.need(), false), true
	case "BODYSTRUCTURE":
		return "BODYSTRUCTURE " + renderBodyStructure(r.need(), true), true
	}
	return "", false
}

// markSeen sets \Seen on a message as the side effect of a body read, and reports
// whether the store took it. The write must be believed before the session is: if
// it fails and the cached row is updated anyway, the client is told the message is
// read while the store still holds it unread, so it disappears from the unread view
// now and comes back on the next SELECT. The failure is recorded because nothing
// else surfaces it: the read itself succeeds either way.
func (c *conn) markSeen(msg *objectstore.MessageInfo, st *objectstore.Store) bool {
	flags := msg.Flags | objectstore.FlagSeen
	if err := st.SetMessageFlags(c.sel.id, msg.UID, flags); err != nil {
		c.event(logging.LevelError, "fetch.seen.fail", logging.Fields{
			"folder": c.sel.id,
			"uid":    msg.UID,
			"error":  err.Error(),
		})
		return false
	}
	msg.Flags = flags
	return true
}

func hasFlagsField(fields []string) bool {
	for _, f := range fields {
		if strings.HasPrefix(f, "FLAGS ") {
			return true
		}
	}
	return false
}

// extractBinary returns a body part decoded from its Content-Transfer-Encoding
// (RFC 3516 BINARY). An empty path addresses the whole message body.
func extractBinary(msg *mime.Part, path []int) ([]byte, bool) {
	part, ok := msg.PartAt(path)
	if !ok {
		return nil, false
	}
	data, err := part.DecodedContent()
	if err != nil {
		return nil, false
	}
	return data, true
}

// binaryLiteral renders decoded data as a FETCH literal, using the literal8
// syntax (~{n}) when the data contains a NUL octet (RFC 3516 / RFC 3501 literal8).
func binaryLiteral(data []byte) string {
	if bytes.IndexByte(data, 0) >= 0 {
		return fmt.Sprintf("~{%d}\r\n%s", len(data), data)
	}
	return fmt.Sprintf("{%d}\r\n%s", len(data), data)
}

// applyPartial trims data to the requested <start.count> octet window.
func applyPartial(data []byte, partial *[2]int) []byte {
	if partial == nil {
		return data
	}
	start, count := partial[0], partial[1]
	if start >= len(data) {
		return []byte{}
	}
	end := min(start+count, len(data))
	return data[start:end]
}

// --- FETCH item parsing ---

var fetchMacros = map[string][]string{
	"ALL":  {"FLAGS", "INTERNALDATE", "RFC822.SIZE", "ENVELOPE"},
	"FAST": {"FLAGS", "INTERNALDATE", "RFC822.SIZE"},
	"FULL": {"FLAGS", "INTERNALDATE", "RFC822.SIZE", "ENVELOPE", "BODY"},
}

// parseFetchItems parses the FETCH item argument: a macro, a single item, or a
// parenthesized list of items.
func parseFetchItems(args []token) ([]fetchItem, error) {
	cur := &tokenCursor{toks: args}
	first, ok := cur.peek()
	if !ok {
		return nil, errProtocol
	}
	if first.kind == tLParen {
		cur.next()
		var items []fetchItem
		for {
			t, ok := cur.peek()
			if !ok {
				return nil, errProtocol
			}
			if t.kind == tRParen {
				break
			}
			it, err := parseOneItem(cur)
			if err != nil {
				return nil, err
			}
			items = append(items, it...)
		}
		return items, nil
	}
	if name, ok := first.str(); ok {
		if macro, isMacro := fetchMacros[strings.ToUpper(name)]; isMacro {
			cur.next()
			return expandNames(macro), nil
		}
	}
	return parseOneItem(cur)
}

// expandNames turns a list of plain item names into fetchItems.
func expandNames(names []string) []fetchItem {
	out := make([]fetchItem, 0, len(names))
	for _, n := range names {
		out = append(out, fetchItem{kind: strings.ToUpper(n)})
	}
	return out
}

// parseOneItem parses a single FETCH item (which may span several tokens for a
// BODY[...] section). It returns a slice to allow RFC822 aliases to expand.
func parseOneItem(cur *tokenCursor) ([]fetchItem, error) {
	t, _ := cur.next()
	name, ok := t.str()
	if !ok {
		return nil, errProtocol
	}
	upper := strings.ToUpper(name)

	if item, ok := plainFetchItems[upper]; ok {
		return []fetchItem{item}, nil
	}
	hasSection := sectionFollows(cur)
	switch upper {
	case "BODY", "BODY.PEEK":
		if !hasSection {
			if upper == "BODY.PEEK" {
				return nil, errProtocol // BODY.PEEK must carry a section
			}
			return []fetchItem{{kind: "BODY"}}, nil // BODY without section = BODYSTRUCTURE
		}
		return parseBodySection(cur, upper == "BODY.PEEK")
	case "BINARY", "BINARY.PEEK":
		if !hasSection {
			return nil, errProtocol // BINARY requires a section
		}
		return parseBinarySection(cur, upper == "BINARY.PEEK", false)
	case "BINARY.SIZE":
		if !hasSection {
			return nil, errProtocol
		}
		return parseBinarySection(cur, true, true) // SIZE is metadata; never sets \Seen
	}
	return nil, fmt.Errorf("%w: unknown FETCH item %q", errProtocol, name)
}

// sectionFollows reports whether the next token opens a section specifier.
func sectionFollows(cur *tokenCursor) bool {
	next, ok := cur.peek()
	return ok && next.kind == tLBracket
}

// plainFetchItems are the items that carry no section and resolve to one fixed
// item. The RFC822 family is the pre-IMAP4rev1 spelling of a whole-message or
// header/text section fetch; RFC822.HEADER peeks, since reading headers alone
// does not mark a message read.
var plainFetchItems = map[string]fetchItem{
	"FLAGS":         {kind: "FLAGS"},
	"INTERNALDATE":  {kind: "INTERNALDATE"},
	"RFC822.SIZE":   {kind: "RFC822.SIZE"},
	"ENVELOPE":      {kind: "ENVELOPE"},
	"BODYSTRUCTURE": {kind: "BODYSTRUCTURE"},
	"UID":           {kind: "UID"},
	"MODSEQ":        {kind: "MODSEQ"},
	"RFC822":        {kind: "SECTION", name: "RFC822", section: mime.Section{}},
	"RFC822.HEADER": {kind: "SECTION", peek: true, name: "RFC822.HEADER", section: mime.Section{Specifier: "HEADER"}},
	"RFC822.TEXT":   {kind: "SECTION", name: "RFC822.TEXT", section: mime.Section{Specifier: "TEXT"}},
}

// parseBinarySection parses BINARY[part]/BINARY.PEEK[part]/BINARY.SIZE[part]
// (RFC 3516). Only a numeric part path is valid, BINARY does not take the
// HEADER/TEXT/MIME specifiers that BODY does.
func parseBinarySection(cur *tokenCursor, peek, sizeOnly bool) ([]fetchItem, error) {
	cur.next() // consume '['
	sec, err := parseSectionSpec(cur)
	if err != nil {
		return nil, err
	}
	if sec.Specifier != "" {
		return nil, fmt.Errorf("%w: BINARY section must be a part number", errProtocol)
	}
	t, ok := cur.next()
	if !ok || t.kind != tRBracket {
		return nil, fmt.Errorf("%w: unterminated binary section", errProtocol)
	}
	label := "BINARY"
	if sizeOnly {
		label = "BINARY.SIZE"
	}
	item := fetchItem{kind: label, peek: peek, section: sec}
	if !sizeOnly {
		if item.partial, err = parseTrailingPartial(cur); err != nil {
			return nil, err
		}
	}
	item.name = label + "[" + sectionString(sec) + "]"
	if item.partial != nil {
		item.name += fmt.Sprintf("<%d>", item.partial[0])
	}
	return []fetchItem{item}, nil
}

// parseBodySection parses BODY[...]<partial> starting at the '['.
func parseBodySection(cur *tokenCursor, peek bool) ([]fetchItem, error) {
	cur.next() // consume '['
	sec, err := parseSectionSpec(cur)
	if err != nil {
		return nil, err
	}
	t, ok := cur.next()
	if !ok || t.kind != tRBracket {
		return nil, fmt.Errorf("%w: unterminated body section", errProtocol)
	}
	item := fetchItem{kind: "SECTION", peek: peek, section: sec}
	if item.partial, err = parseTrailingPartial(cur); err != nil {
		return nil, err
	}
	item.name = "BODY[" + sectionString(sec) + "]"
	if item.partial != nil {
		item.name += fmt.Sprintf("<%d>", item.partial[0])
	}
	return []fetchItem{item}, nil
}

// parseTrailingPartial consumes a <offset.count> partial specifier when one
// follows the section, and answers nil when none does.
func parseTrailingPartial(cur *tokenCursor) (*[2]int, error) {
	p, ok := cur.peek()
	if !ok || p.kind != tAtom || !strings.HasPrefix(p.val, "<") {
		return nil, nil
	}
	cur.next()
	return parsePartial(p.val)
}

// parseSectionSpec parses the tokens inside BODY[...] into a mime.Section.
func parseSectionSpec(cur *tokenCursor) (mime.Section, error) {
	t, ok := cur.peek()
	if !ok {
		return mime.Section{}, errProtocol
	}
	if t.kind == tRBracket {
		return mime.Section{}, nil // BODY[]
	}
	spec, ok := t.str()
	if !ok {
		return mime.Section{}, errProtocol
	}
	cur.next()
	sec := splitSpec(spec)
	// HEADER.FIELDS / HEADER.FIELDS.NOT carry a parenthesized field list.
	if strings.HasPrefix(strings.ToUpper(sec.Specifier), "HEADER.FIELDS") {
		fields, err := parseFieldList(cur)
		if err != nil {
			return mime.Section{}, err
		}
		sec.Fields = fields
	}
	return sec, nil
}

// splitSpec parses a section specifier like "1.2.MIME" or "HEADER.FIELDS" into
// its numeric path and trailing keyword.
func splitSpec(spec string) mime.Section {
	var sec mime.Section
	parts := strings.Split(spec, ".")
	i := 0
	for i < len(parts) {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			break
		}
		sec.Path = append(sec.Path, n)
		i++
	}
	if i < len(parts) {
		sec.Specifier = strings.ToUpper(strings.Join(parts[i:], "."))
	}
	return sec
}

// parseFieldList reads a parenthesized header-field-name list.
func parseFieldList(cur *tokenCursor) ([]string, error) {
	t, ok := cur.next()
	if !ok || t.kind != tLParen {
		return nil, fmt.Errorf("%w: HEADER.FIELDS requires a field list", errProtocol)
	}
	var fields []string
	for {
		t, ok := cur.next()
		if !ok {
			return nil, errProtocol
		}
		if t.kind == tRParen {
			return fields, nil
		}
		if s, ok := t.str(); ok {
			fields = append(fields, s)
		}
	}
}

// parsePartial parses a "<start.count>" partial specifier.
func parsePartial(s string) (*[2]int, error) {
	inner := strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">")
	a, b, found := strings.Cut(inner, ".")
	start, err := strconv.Atoi(a)
	if err != nil || !found {
		return nil, fmt.Errorf("%w: bad partial %q", errProtocol, s)
	}
	count, err := strconv.Atoi(b)
	if err != nil {
		return nil, fmt.Errorf("%w: bad partial %q", errProtocol, s)
	}
	return &[2]int{start, count}, nil
}

// sectionString renders a mime.Section back to its BODY[...] inner text.
func sectionString(s mime.Section) string {
	var sb strings.Builder
	for i, n := range s.Path {
		if i > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(strconv.Itoa(n))
	}
	if s.Specifier != "" {
		if len(s.Path) > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(s.Specifier)
		if len(s.Fields) > 0 {
			sb.WriteString(" (")
			sb.WriteString(strings.Join(s.Fields, " "))
			sb.WriteByte(')')
		}
	}
	return sb.String()
}
