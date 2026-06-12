package imap

import (
	"bufio"
	"bytes"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"hermex/internal/store"
)

// matcher tests whether a message satisfies a search key.
type matcher func(*searchCtx) bool

// searchCtx evaluates search keys against one message, loading and parsing the
// raw message lazily (only header/body keys need it).
type searchCtx struct {
	seq    uint32
	msg    store.MessageInfo
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
	s.raw, _ = s.c.st.GetMessageRaw(s.c.sel.id, s.msg.UID)
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
	var results []uint32
	for i := range c.sel.msgs {
		sc := &searchCtx{seq: uint32(i + 1), msg: c.sel.msgs[i], c: c}
		if m(sc) {
			if byUID {
				results = append(results, sc.msg.UID)
			} else {
				results = append(results, sc.seq)
			}
		}
	}
	if len(results) == 0 {
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
	switch key {
	case "ALL":
		return func(*searchCtx) bool { return true }, nil
	case "ANSWERED":
		return flagSet(store.FlagAnswered, true), nil
	case "UNANSWERED":
		return flagSet(store.FlagAnswered, false), nil
	case "DELETED":
		return flagSet(store.FlagDeleted, true), nil
	case "UNDELETED":
		return flagSet(store.FlagDeleted, false), nil
	case "DRAFT":
		return flagSet(store.FlagDraft, true), nil
	case "UNDRAFT":
		return flagSet(store.FlagDraft, false), nil
	case "FLAGGED":
		return flagSet(store.FlagFlagged, true), nil
	case "UNFLAGGED":
		return flagSet(store.FlagFlagged, false), nil
	case "SEEN":
		return flagSet(store.FlagSeen, true), nil
	case "UNSEEN":
		return flagSet(store.FlagSeen, false), nil
	case "NEW", "RECENT":
		// \Recent is never set, so these never match.
		return func(*searchCtx) bool { return false }, nil
	case "OLD":
		return func(*searchCtx) bool { return true }, nil
	case "KEYWORD":
		cur.next() // a keyword is never stored, so it never matches
		return func(*searchCtx) bool { return false }, nil
	case "UNKEYWORD":
		cur.next()
		return func(*searchCtx) bool { return true }, nil
	case "NOT":
		sub, err := parseSearchKey(cur)
		if err != nil {
			return nil, err
		}
		return func(s *searchCtx) bool { return !sub(s) }, nil
	case "OR":
		a, err := parseSearchKey(cur)
		if err != nil {
			return nil, err
		}
		b, err := parseSearchKey(cur)
		if err != nil {
			return nil, err
		}
		return func(s *searchCtx) bool { return a(s) || b(s) }, nil
	case "UID":
		return seqMatch(cur, true)
	case "FROM", "TO", "CC", "BCC", "SUBJECT":
		return headerMatch(cur, key)
	case "HEADER":
		field, ok1 := cur.next()
		value, ok2 := cur.next()
		if !ok1 || !ok2 {
			return nil, errProtocol
		}
		return headerContains(field.val, value.val), nil
	case "BODY":
		v, ok := cur.next()
		if !ok {
			return nil, errProtocol
		}
		needle := strings.ToLower(v.val)
		return func(s *searchCtx) bool { s.load(); return strings.Contains(strings.ToLower(string(s.body)), needle) }, nil
	case "TEXT":
		v, ok := cur.next()
		if !ok {
			return nil, errProtocol
		}
		needle := strings.ToLower(v.val)
		return func(s *searchCtx) bool { s.load(); return strings.Contains(strings.ToLower(string(s.raw)), needle) }, nil
	case "LARGER", "SMALLER":
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
	case "SINCE", "BEFORE", "ON":
		return dateMatch(cur, key, false)
	case "SENTSINCE", "SENTBEFORE", "SENTON":
		return dateMatch(cur, key, true)
	default:
		// A bare token is a sequence set (message numbers).
		set, err := parseSeqSet(t.val)
		if err != nil {
			return nil, errProtocol
		}
		return func(s *searchCtx) bool { return set.contains(s.seq, s.c.sel.maxSeq()) }, nil
	}
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
// case-insensitively. An empty needle matches any message that has the field.
func headerContains(field, needle string) matcher {
	low := strings.ToLower(needle)
	return func(s *searchCtx) bool {
		return strings.Contains(strings.ToLower(s.header(field)), low)
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
