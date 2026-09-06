package imap

import (
	"bufio"
	"bytes"
	"fmt"
	"net/mail"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"

	"hermex/internal/objectstore"
)

// matcher tests whether a message satisfies a search key.
type matcher func(*searchCtx) bool

// searchCtx evaluates search keys against one message, loading and parsing the
// raw message lazily (only header/body keys need it).
type searchCtx struct {
	seq    uint32
	msg    objectstore.MessageInfo
	c      *conn
	raw    []byte
	hdr    textproto.MIMEHeader
	body   []byte
	loaded bool
}

func (s *searchCtx) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.raw, _ = s.c.curStore().GetMessageRaw(s.c.sel.id, s.msg.UID)
	off := bodyStart(s.raw)
	tr := textproto.NewReader(bufio.NewReader(bytes.NewReader(s.raw[:off])))
	s.hdr, _ = tr.ReadMIMEHeader()
	s.body = s.raw[off:]
}

// bodyStart returns the index of the body within a raw message.
func bodyStart(raw []byte) int {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return i + 4
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return i + 2
	}
	return len(raw)
}

func (s *searchCtx) header(name string) string {
	s.load()
	return strings.Join(s.hdr.Values(name), " ")
}

// cmdSearch handles SEARCH and (byUID) UID SEARCH.
func (c *conn) cmdSearch(tag string, args []token, byUID bool) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	cur := &tokenCursor{toks: args}
	// An optional RETURN (...) clause (RFC 4731 ESEARCH) precedes any CHARSET.
	returnOpts, hasReturn := parseReturnOpts(cur)
	// An optional CHARSET specifier precedes the keys; we accept and ignore it.
	if t, ok := cur.peek(); ok && t.isAtom("CHARSET") {
		cur.next()
		cur.next()
	}
	m, err := parseSearchKeys(cur)
	if err != nil {
		c.bad(tag, "invalid SEARCH criteria")
		return
	}
	results := c.searchMatches(m, byUID)
	if hasReturn {
		c.writeESearch(tag, byUID, returnOpts, results)
	} else if len(results) == 0 {
		c.untagged("SEARCH")
	} else {
		c.untagged("SEARCH %s", ids(results))
	}
	verb := "SEARCH"
	if byUID {
		verb = "UID SEARCH"
	}
	c.ok(tag, verb+" completed")
}

// searchMatches runs the criteria over the selected mailbox and returns the
// matching UIDs (byUID) or message numbers.
func (c *conn) searchMatches(m matcher, byUID bool) []uint32 {
	var results []uint32
	for i := range c.sel.msgs {
		// #nosec G115 -- an IMAP sequence number, an index into the selected mailbox's in-memory message list
		sc := &searchCtx{seq: uint32(i + 1), msg: c.sel.msgs[i], c: c}
		if !m(sc) {
			continue
		}
		if byUID {
			results = append(results, sc.msg.UID)
		} else {
			results = append(results, sc.seq)
		}
	}
	return results
}

// parseReturnOpts consumes an RFC 4731 "RETURN (opt ...)" clause if present,
// returning the uppercased option names. An empty list defaults to ALL.
func parseReturnOpts(cur *tokenCursor) (opts []string, present bool) {
	t, ok := cur.peek()
	if !ok || !t.isAtom("RETURN") {
		return nil, false
	}
	cur.next() // RETURN
	open, ok := cur.next()
	if !ok || open.kind != tLParen {
		return nil, true // malformed; treated as ESEARCH with default options
	}
	for {
		t, ok := cur.next()
		if !ok || t.kind == tRParen {
			break
		}
		opts = append(opts, strings.ToUpper(t.val))
	}
	if len(opts) == 0 {
		opts = []string{"ALL"}
	}
	return opts, true
}

// writeESearch emits the RFC 4731 ESEARCH response, including only the requested
// result options. MIN/MAX/ALL are omitted when there are no matches; COUNT is
// always reportable.
func (c *conn) writeESearch(tag string, byUID bool, opts []string, results []uint32) {
	want := func(o string) bool {
		return slices.Contains(opts, o)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "ESEARCH (TAG %s)", safeQuoted(tag))
	if byUID {
		sb.WriteString(" UID")
	}
	if want("COUNT") {
		fmt.Fprintf(&sb, " COUNT %d", len(results))
	}
	if len(results) > 0 {
		if want("MIN") {
			fmt.Fprintf(&sb, " MIN %d", results[0])
		}
		if want("MAX") {
			fmt.Fprintf(&sb, " MAX %d", results[len(results)-1])
		}
		if want("ALL") {
			fmt.Fprintf(&sb, " ALL %s", esearchSet(results))
		}
	}
	c.untagged("%s", sb.String())
}

// esearchSet compresses an ascending id list into an IMAP sequence-set, collapsing
// consecutive runs into ranges (e.g. 2,4:7,9).
func esearchSet(ns []uint32) string {
	var parts []string
	for i := 0; i < len(ns); {
		j := i
		for j+1 < len(ns) && ns[j+1] == ns[j]+1 {
			j++
		}
		if i == j {
			parts = append(parts, fmt.Sprintf("%d", ns[i]))
		} else {
			parts = append(parts, fmt.Sprintf("%d:%d", ns[i], ns[j]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// parseSearchKeys parses a sequence of search keys joined by implicit AND, up to
// the end of input or a closing parenthesis.
func parseSearchKeys(cur *tokenCursor) (matcher, error) {
	var ms []matcher
	for {
		t, ok := cur.peek()
		if !ok || t.kind == tRParen {
			break
		}
		m, err := parseSearchKey(cur)
		if err != nil {
			return nil, err
		}
		ms = append(ms, m)
	}
	if len(ms) == 0 {
		return nil, errProtocol
	}
	return func(s *searchCtx) bool {
		for _, m := range ms {
			if !m(s) {
				return false
			}
		}
		return true
	}, nil
}

// parseSearchKey parses one search key (RFC 3501 §6.4.4), recursing for NOT, OR,
// and parenthesized groups.
func parseSearchKey(cur *tokenCursor) (matcher, error) {
	t, ok := cur.next()
	if !ok {
		return nil, errProtocol
	}
	if t.kind == tLParen {
		inner, err := parseSearchKeys(cur)
		if err != nil {
			return nil, err
		}
		if end, ok := cur.next(); !ok || end.kind != tRParen {
			return nil, errProtocol
		}
		return inner, nil
	}

	key := strings.ToUpper(t.val)
	if m, ok := constantSearchKeys[key]; ok {
		return m, nil
	}
	if parse, ok := searchKeyParsers[key]; ok {
		return parse(cur, key)
	}
	// A bare token is a sequence set (message numbers).
	set, err := parseSeqSet(t.val)
	if err != nil {
		return nil, errProtocol
	}
	return func(s *searchCtx) bool { return set.contains(s.seq, s.c.sel.maxSeq()) }, nil
}

// matchAll and matchNone are the two constant search results.
func matchAll(*searchCtx) bool  { return true }
func matchNone(*searchCtx) bool { return false }

// constantSearchKeys are the keys that take no argument and resolve to a fixed
// matcher. \Recent is never set, so NEW and RECENT never match and OLD always does.
var constantSearchKeys = map[string]matcher{
	"ALL":        matchAll,
	"ANSWERED":   flagSet(objectstore.FlagAnswered, true),
	"UNANSWERED": flagSet(objectstore.FlagAnswered, false),
	"DELETED":    flagSet(objectstore.FlagDeleted, true),
	"UNDELETED":  flagSet(objectstore.FlagDeleted, false),
	"DRAFT":      flagSet(objectstore.FlagDraft, true),
	"UNDRAFT":    flagSet(objectstore.FlagDraft, false),
	"FLAGGED":    flagSet(objectstore.FlagFlagged, true),
	"UNFLAGGED":  flagSet(objectstore.FlagFlagged, false),
	"SEEN":       flagSet(objectstore.FlagSeen, true),
	"UNSEEN":     flagSet(objectstore.FlagSeen, false),
	"NEW":        matchNone,
	"RECENT":     matchNone,
	"OLD":        matchAll,
}

// searchKeyParsers are the keys that read further tokens. Each is handed the
// cursor and the upper-cased key, since several keys share one parser and differ
// only by it.
var searchKeyParsers = map[string]func(*tokenCursor, string) (matcher, error){
	"KEYWORD":    parseKeywordKey,
	"UNKEYWORD":  parseKeywordKey,
	"UID":        func(cur *tokenCursor, _ string) (matcher, error) { return seqMatch(cur, true) },
	"FROM":       headerMatch,
	"TO":         headerMatch,
	"CC":         headerMatch,
	"BCC":        headerMatch,
	"SUBJECT":    headerMatch,
	"HEADER":     parseHeaderKey,
	"BODY":       parseTextKey,
	"TEXT":       parseTextKey,
	"LARGER":     parseSizeKey,
	"SMALLER":    parseSizeKey,
	"SINCE":      func(cur *tokenCursor, key string) (matcher, error) { return dateMatch(cur, key, false) },
	"BEFORE":     func(cur *tokenCursor, key string) (matcher, error) { return dateMatch(cur, key, false) },
	"ON":         func(cur *tokenCursor, key string) (matcher, error) { return dateMatch(cur, key, false) },
	"SENTSINCE":  func(cur *tokenCursor, key string) (matcher, error) { return dateMatch(cur, key, true) },
	"SENTBEFORE": func(cur *tokenCursor, key string) (matcher, error) { return dateMatch(cur, key, true) },
	"SENTON":     func(cur *tokenCursor, key string) (matcher, error) { return dateMatch(cur, key, true) },
}

// NOT and OR recurse back into parseSearchKey, which reads this same table, so
// they are registered here rather than in the literal above (a package-level
// literal naming them would be an initialization cycle).
func init() {
	searchKeyParsers["NOT"] = parseNotKey
	searchKeyParsers["OR"] = parseOrKey
}

// parseKeywordKey consumes the keyword and resolves to a constant: a keyword is
// never stored, so KEYWORD never matches and UNKEYWORD always does.
func parseKeywordKey(cur *tokenCursor, key string) (matcher, error) {
	cur.next()
	if key == "KEYWORD" {
		return matchNone, nil
	}
	return matchAll, nil
}

// parseNotKey inverts the key that follows.
func parseNotKey(cur *tokenCursor, _ string) (matcher, error) {
	sub, err := parseSearchKey(cur)
	if err != nil {
		return nil, err
	}
	return func(s *searchCtx) bool { return !sub(s) }, nil
}

// parseOrKey matches either of the two keys that follow.
func parseOrKey(cur *tokenCursor, _ string) (matcher, error) {
	a, err := parseSearchKey(cur)
	if err != nil {
		return nil, err
	}
	b, err := parseSearchKey(cur)
	if err != nil {
		return nil, err
	}
	return func(s *searchCtx) bool { return a(s) || b(s) }, nil
}

// parseHeaderKey matches a client-named header against a value.
func parseHeaderKey(cur *tokenCursor, _ string) (matcher, error) {
	field, ok1 := cur.next()
	value, ok2 := cur.next()
	if !ok1 || !ok2 {
		return nil, errProtocol
	}
	return headerContains(field.val, value.val), nil
}

// parseTextKey matches a substring against the body (BODY) or the whole message
// including its headers (TEXT).
func parseTextKey(cur *tokenCursor, key string) (matcher, error) {
	v, ok := cur.next()
	if !ok {
		return nil, errProtocol
	}
	needle := strings.ToLower(v.val)
	if key == "BODY" {
		return func(s *searchCtx) bool { s.load(); return strings.Contains(strings.ToLower(string(s.body)), needle) }, nil
	}
	return func(s *searchCtx) bool { s.load(); return strings.Contains(strings.ToLower(string(s.raw)), needle) }, nil
}

// parseSizeKey matches messages above (LARGER) or below (SMALLER) a byte count.
func parseSizeKey(cur *tokenCursor, key string) (matcher, error) {
	v, ok := cur.next()
	if !ok {
		return nil, errProtocol
	}
	n, err := strconv.ParseInt(v.val, 10, 64)
	if err != nil {
		return nil, errProtocol
	}
	larger := key == "LARGER"
	return func(s *searchCtx) bool {
		if larger {
			return s.msg.Size > n
		}
		return s.msg.Size < n
	}, nil
}

// flagSet matches messages whose flag bit is set (or clear, when want=false).
func flagSet(bit int64, want bool) matcher {
	return func(s *searchCtx) bool { return (s.msg.Flags&bit != 0) == want }
}

// seqMatch parses a sequence set and matches by UID (uid=true) or message
// number.
func seqMatch(cur *tokenCursor, uid bool) (matcher, error) {
	t, ok := cur.next()
	if !ok {
		return nil, errProtocol
	}
	set, err := parseSeqSet(t.val)
	if err != nil {
		return nil, err
	}
	return func(s *searchCtx) bool {
		if uid {
			return set.contains(s.msg.UID, s.c.sel.maxUID())
		}
		return set.contains(s.seq, s.c.sel.maxSeq())
	}, nil
}

// headerMatch reads the search string and matches it against a named header.
func headerMatch(cur *tokenCursor, field string) (matcher, error) {
	v, ok := cur.next()
	if !ok {
		return nil, errProtocol
	}
	return headerContains(field, v.val), nil
}

// headerContains matches messages whose header field contains needle,
// case-insensitively. Per RFC 3501, an empty needle matches any message that
// merely HAS the field, so an absent field never matches.
func headerContains(field, needle string) matcher {
	low := strings.ToLower(needle)
	return func(s *searchCtx) bool {
		s.load()
		vals := s.hdr.Values(field)
		if len(vals) == 0 {
			return false
		}
		return strings.Contains(strings.ToLower(strings.Join(vals, " ")), low)
	}
}

// dateMatch parses a date and compares it against the internal date (sent=false)
// or the Date header (sent=true).
func dateMatch(cur *tokenCursor, key string, sent bool) (matcher, error) {
	v, ok := cur.next()
	if !ok {
		return nil, errProtocol
	}
	d, err := time.Parse("2-Jan-2006", v.val)
	if err != nil {
		return nil, errProtocol
	}
	return func(s *searchCtx) bool {
		var t time.Time
		if sent {
			parsed, err := mail.ParseDate(s.header("Date"))
			if err != nil {
				return false
			}
			t = parsed
		} else {
			t = s.msg.InternalDate
		}
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		switch key {
		case "SINCE", "SENTSINCE":
			return !day.Before(d)
		case "BEFORE", "SENTBEFORE":
			return day.Before(d)
		default: // ON / SENTON
			return day.Equal(d)
		}
	}, nil
}
