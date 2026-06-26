package imap

import (
	"fmt"
	"strconv"
	"strings"

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
	items, err := parseFetchItems(args[1:])
	if err != nil {
		c.bad(tag, "invalid FETCH items")
		return
	}
	if byUID && !hasKind(items, "UID") {
		items = append(items, fetchItem{kind: "UID"}) // UID FETCH always returns UID
	}

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
		c.writeFetch(seq, i, items)
	}
	verb := "FETCH"
	if byUID {
		verb = "UID FETCH"
	}
	c.ok(tag, verb+" completed")
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
	switch strings.ToUpper(sub) {
	case "FETCH":
		c.cmdFetch(tag, args[1:], true)
	case "STORE":
		c.cmdStore(tag, args[1:], true)
	case "SEARCH":
		c.cmdSearch(tag, args[1:], true)
	case "COPY":
		c.cmdCopy(tag, args[1:], true)
	case "EXPUNGE":
		c.cmdUIDExpunge(tag, args[1:])
	case "MOVE":
		c.cmdMove(tag, args[1:], true)
	default:
		c.bad(tag, "UID "+sub+" not supported")
	}
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
func (c *conn) writeFetch(seq uint32, idx int, items []fetchItem) {
	msg := c.sel.msgs[idx]
	var raw []byte
	var rawLoaded bool
	var structure *mime.Part
	loadRaw := func() []byte {
		if !rawLoaded {
			raw, _ = c.curStore().GetMessageRaw(c.sel.id, msg.UID)
			rawLoaded = true
		}
		return raw
	}
	need := func() *mime.Part {
		if structure == nil {
			structure = mime.ParseStructure(loadRaw())
		}
		return structure
	}

	var fields []string
	setSeen := false
	for _, it := range items {
		switch it.kind {
		case "UID":
			fields = append(fields, fmt.Sprintf("UID %d", msg.UID))
		case "FLAGS":
			fields = append(fields, fmt.Sprintf(`FLAGS (%s)`, formatFlags(msg.Flags, false)))
		case "INTERNALDATE":
			fields = append(fields, `INTERNALDATE `+quoteString(msg.InternalDate.Format("02-Jan-2006 15:04:05 -0700")))
		case "RFC822.SIZE":
			fields = append(fields, fmt.Sprintf("RFC822.SIZE %d", msg.Size))
		case "ENVELOPE":
			env, _ := mime.ParseEnvelope(loadRaw())
			fields = append(fields, "ENVELOPE "+renderEnvelope(env))
		case "BODY":
			fields = append(fields, "BODY "+renderBodyStructure(need(), false))
		case "BODYSTRUCTURE":
			fields = append(fields, "BODYSTRUCTURE "+renderBodyStructure(need(), true))
		case "SECTION":
			data, ok := need().Extract(it.section)
			if !ok {
				data = []byte{}
			}
			data = applyPartial(data, it.partial)
			fields = append(fields, it.name+" "+literalize(string(data)))
			if !it.peek {
				setSeen = true
			}
		}
	}

	// A read-only selection (EXAMINE, or a public folder the caller cannot post to)
	// must not implicitly set \Seen — and must never write to the public store.
	if setSeen && !c.readOnly && msg.Flags&objectstore.FlagSeen == 0 {
		msg.Flags |= objectstore.FlagSeen
		c.curStore().SetMessageFlags(c.sel.id, msg.UID, msg.Flags)
		c.sel.msgs[idx].Flags = msg.Flags
		if !hasFlagsField(fields) {
			fields = append(fields, fmt.Sprintf(`FLAGS (%s)`, formatFlags(msg.Flags, false)))
		}
	}

	fmt.Fprintf(c.bw, "* %d FETCH (%s)\r\n", seq, strings.Join(fields, " "))
	c.flush()
}

func hasFlagsField(fields []string) bool {
	for _, f := range fields {
		if strings.HasPrefix(f, "FLAGS ") {
			return true
		}
	}
	return false
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

	switch upper {
	case "FLAGS", "INTERNALDATE", "RFC822.SIZE", "ENVELOPE", "BODYSTRUCTURE", "UID":
		return []fetchItem{{kind: upper}}, nil
	case "RFC822":
		return []fetchItem{{kind: "SECTION", name: "RFC822", section: mime.Section{}}}, nil
	case "RFC822.HEADER":
		return []fetchItem{{kind: "SECTION", peek: true, name: "RFC822.HEADER", section: mime.Section{Specifier: "HEADER"}}}, nil
	case "RFC822.TEXT":
		return []fetchItem{{kind: "SECTION", name: "RFC822.TEXT", section: mime.Section{Specifier: "TEXT"}}}, nil
	case "BODY", "BODY.PEEK":
		if next, ok := cur.peek(); !ok || next.kind != tLBracket {
			if upper == "BODY.PEEK" {
				return nil, errProtocol // BODY.PEEK must carry a section
			}
			return []fetchItem{{kind: "BODY"}}, nil // BODY without section = BODYSTRUCTURE
		}
		return parseBodySection(cur, upper == "BODY.PEEK")
	}
	return nil, fmt.Errorf("%w: unknown FETCH item %q", errProtocol, name)
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
	if p, ok := cur.peek(); ok && p.kind == tAtom && strings.HasPrefix(p.val, "<") {
		cur.next()
		if item.partial, err = parsePartial(p.val); err != nil {
			return nil, err
		}
	}
	item.name = "BODY[" + sectionString(sec) + "]"
	if item.partial != nil {
		item.name += fmt.Sprintf("<%d>", item.partial[0])
	}
	return []fetchItem{item}, nil
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
