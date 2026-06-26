package imap

import (
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"
)

// SORT and THREAD (RFC 5256). Both run the SEARCH criteria first, then reorder the
// matches: SORT by a list of sort keys, THREAD into a nested thread structure.

// --- SORT ---

type sortCrit struct {
	key     string
	reverse bool
}

// parseSortCriteria consumes the "(crit ...)" SORT criteria list; REVERSE applies
// to the criterion that follows it.
func parseSortCriteria(cur *tokenCursor) ([]sortCrit, bool) {
	open, ok := cur.next()
	if !ok || open.kind != tLParen {
		return nil, false
	}
	var crits []sortCrit
	reverse := false
	for {
		t, ok := cur.next()
		if !ok {
			return nil, false
		}
		if t.kind == tRParen {
			break
		}
		if key := strings.ToUpper(t.val); key == "REVERSE" {
			reverse = true
		} else {
			crits = append(crits, sortCrit{key: key, reverse: reverse})
			reverse = false
		}
	}
	if len(crits) == 0 {
		return nil, false
	}
	return crits, true
}

// sortKeys holds one message's sortable attributes.
type sortKeys struct {
	idx     int
	arrival time.Time
	date    time.Time
	size    int64
	from    string
	to      string
	cc      string
	subject string
}

// cmdSort handles SORT and (byUID) UID SORT (RFC 5256): it searches, orders the
// matches by the criteria, and returns the ordered ids.
func (c *conn) cmdSort(tag string, args []token, byUID bool) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	cur := &tokenCursor{toks: args}
	crits, ok := parseSortCriteria(cur)
	if !ok {
		c.bad(tag, "invalid SORT criteria")
		return
	}
	if _, ok := cur.next(); !ok { // a charset is mandatory in the grammar; ignore it
		c.bad(tag, "SORT requires a charset and search keys")
		return
	}
	m, err := parseSearchKeys(cur)
	if err != nil {
		c.bad(tag, "invalid SORT criteria")
		return
	}
	needHdr := sortNeedsHeader(crits)
	var keys []sortKeys
	for i := range c.sel.msgs {
		sc := &searchCtx{seq: uint32(i + 1), msg: c.sel.msgs[i], c: c}
		if m(sc) {
			keys = append(keys, c.sortKeysFor(i, needHdr))
		}
	}
	sort.SliceStable(keys, func(a, b int) bool { return lessSort(keys[a], keys[b], crits) })

	var out []uint32
	for _, k := range keys {
		out = append(out, c.threadID(k.idx, byUID))
	}
	if len(out) == 0 {
		c.untagged("SORT")
	} else {
		c.untagged("SORT %s", ids(out))
	}
	verb := "SORT"
	if byUID {
		verb = "UID SORT"
	}
	c.ok(tag, verb+" completed")
}

func sortNeedsHeader(crits []sortCrit) bool {
	for _, cr := range crits {
		switch cr.key {
		case "DATE", "FROM", "TO", "CC", "SUBJECT":
			return true
		}
	}
	return false
}

// sortKeysFor builds the sortable attributes for message index i, loading the
// header only when a header-based criterion needs it.
func (c *conn) sortKeysFor(i int, needHdr bool) sortKeys {
	m := c.sel.msgs[i]
	k := sortKeys{idx: i, arrival: m.InternalDate, date: m.InternalDate, size: m.Size}
	if needHdr {
		sc := &searchCtx{seq: uint32(i + 1), msg: m, c: c}
		k.from = sortAddr(sc.header("From"))
		k.to = sortAddr(sc.header("To"))
		k.cc = sortAddr(sc.header("Cc"))
		k.subject = baseSubject(sc.header("Subject"))
		if d, err := mail.ParseDate(sc.header("Date")); err == nil {
			k.date = d
		}
	}
	return k
}

func lessSort(a, b sortKeys, crits []sortCrit) bool {
	for _, cr := range crits {
		cmp := 0
		switch cr.key {
		case "ARRIVAL":
			cmp = a.arrival.Compare(b.arrival)
		case "DATE":
			cmp = a.date.Compare(b.date)
		case "SIZE":
			switch {
			case a.size < b.size:
				cmp = -1
			case a.size > b.size:
				cmp = 1
			}
		case "FROM":
			cmp = strings.Compare(a.from, b.from)
		case "TO":
			cmp = strings.Compare(a.to, b.to)
		case "CC":
			cmp = strings.Compare(a.cc, b.cc)
		case "SUBJECT":
			cmp = strings.Compare(a.subject, b.subject)
		}
		if cr.reverse {
			cmp = -cmp
		}
		if cmp != 0 {
			return cmp < 0
		}
	}
	return a.idx < b.idx
}

// sortAddr extracts the lowercased mailbox address used for FROM/TO/CC sorting,
// falling back to the raw header when it cannot be parsed.
func sortAddr(hdr string) string {
	if hdr == "" {
		return ""
	}
	if addr, err := mail.ParseAddress(hdr); err == nil {
		return strings.ToLower(addr.Address)
	}
	return strings.ToLower(strings.TrimSpace(hdr))
}

// baseSubject strips leading "Re:"/"Fwd:"/"Fw:" prefixes to the RFC 5256 base
// subject used for sorting and ORDEREDSUBJECT threading.
func baseSubject(subj string) string {
	s := strings.TrimSpace(subj)
	for {
		low := strings.ToLower(s)
		switch {
		case strings.HasPrefix(low, "re:"):
			s = strings.TrimSpace(s[3:])
		case strings.HasPrefix(low, "fwd:"):
			s = strings.TrimSpace(s[4:])
		case strings.HasPrefix(low, "fw:"):
			s = strings.TrimSpace(s[3:])
		default:
			return strings.ToLower(s)
		}
	}
}

// --- THREAD ---

// threadID renders a message's THREAD/SORT id (UID or sequence number).
func (c *conn) threadID(idx int, byUID bool) uint32 {
	if byUID {
		return c.sel.msgs[idx].UID
	}
	return uint32(idx + 1)
}

// cmdThread handles THREAD and (byUID) UID THREAD (RFC 5256): it groups the
// matching messages into threads and returns the nested thread structure.
func (c *conn) cmdThread(tag string, args []token, byUID bool) {
	if c.state != stateSelected {
		c.no(tag, "no mailbox selected")
		return
	}
	cur := &tokenCursor{toks: args}
	algoTok, ok := cur.next()
	if !ok {
		c.bad(tag, "THREAD requires an algorithm")
		return
	}
	algo := strings.ToUpper(algoTok.val)
	if _, ok := cur.next(); !ok { // charset
		c.bad(tag, "THREAD requires a charset and search keys")
		return
	}
	m, err := parseSearchKeys(cur)
	if err != nil {
		c.bad(tag, "invalid THREAD criteria")
		return
	}
	var matched []int
	for i := range c.sel.msgs {
		sc := &searchCtx{seq: uint32(i + 1), msg: c.sel.msgs[i], c: c}
		if m(sc) {
			matched = append(matched, i)
		}
	}

	var structure string
	switch algo {
	case "ORDEREDSUBJECT":
		structure = c.threadOrderedSubject(matched, byUID)
	case "REFERENCES":
		structure = c.threadReferences(matched, byUID)
	default:
		c.bad(tag, "unsupported THREAD algorithm")
		return
	}
	c.untagged("THREAD %s", structure)
	verb := "THREAD"
	if byUID {
		verb = "UID THREAD"
	}
	c.ok(tag, verb+" completed")
}

// threadMsg is a matched message with the attributes threading needs.
type threadMsg struct {
	idx     int
	date    time.Time
	subject string
	msgID   string
	refs    []string
}

func (c *conn) threadMsgs(matched []int) []threadMsg {
	out := make([]threadMsg, 0, len(matched))
	for _, i := range matched {
		sc := &searchCtx{seq: uint32(i + 1), msg: c.sel.msgs[i], c: c}
		d := c.sel.msgs[i].InternalDate
		if pd, err := mail.ParseDate(sc.header("Date")); err == nil {
			d = pd
		}
		out = append(out, threadMsg{
			idx:     i,
			date:    d,
			subject: baseSubject(sc.header("Subject")),
			msgID:   firstAngle(sc.header("Message-ID")),
			refs:    allAngles(sc.header("References") + " " + sc.header("In-Reply-To")),
		})
	}
	return out
}

// threadOrderedSubject groups messages by base subject; each thread lists its
// members in date order, and threads are ordered by their earliest member.
func (c *conn) threadOrderedSubject(matched []int, byUID bool) string {
	msgs := c.threadMsgs(matched)
	groups := map[string][]threadMsg{}
	var order []string
	for _, mm := range msgs {
		if _, seen := groups[mm.subject]; !seen {
			order = append(order, mm.subject)
		}
		groups[mm.subject] = append(groups[mm.subject], mm)
	}
	type thread struct {
		members  []threadMsg
		earliest time.Time
	}
	var threads []thread
	for _, subj := range order {
		g := groups[subj]
		sort.SliceStable(g, func(a, b int) bool { return g[a].date.Before(g[b].date) })
		threads = append(threads, thread{members: g, earliest: g[0].date})
	}
	sort.SliceStable(threads, func(a, b int) bool { return threads[a].earliest.Before(threads[b].earliest) })

	var sb strings.Builder
	for _, th := range threads {
		sb.WriteString("(")
		for i, mm := range th.members {
			if i > 0 {
				sb.WriteString(" ")
			}
			fmt.Fprintf(&sb, "%d", c.threadID(mm.idx, byUID))
		}
		sb.WriteString(")")
	}
	return sb.String()
}

// tnode is a node in the REFERENCES thread forest.
type tnode struct {
	idx      int
	date     time.Time
	children []*tnode
}

// threadReferences builds threads by linking each message to the last of its
// References/In-Reply-To message-ids that is present in the matched set (a
// simplification of RFC 5256 that does not synthesize phantom parents).
func (c *conn) threadReferences(matched []int, byUID bool) string {
	msgs := c.threadMsgs(matched)
	nodes := make(map[int]*tnode, len(msgs))
	byMsgID := map[string]int{}
	for _, mm := range msgs {
		nodes[mm.idx] = &tnode{idx: mm.idx, date: mm.date}
		if mm.msgID != "" {
			byMsgID[mm.msgID] = mm.idx
		}
	}
	var roots []*tnode
	for _, mm := range msgs {
		parent := -1
		for j := len(mm.refs) - 1; j >= 0; j-- {
			if pi, ok := byMsgID[mm.refs[j]]; ok && pi != mm.idx {
				parent = pi
				break
			}
		}
		if parent >= 0 {
			nodes[parent].children = append(nodes[parent].children, nodes[mm.idx])
		} else {
			roots = append(roots, nodes[mm.idx])
		}
	}
	var sortTree func(n *tnode)
	sortTree = func(n *tnode) {
		sort.SliceStable(n.children, func(a, b int) bool { return n.children[a].date.Before(n.children[b].date) })
		for _, ch := range n.children {
			sortTree(ch)
		}
	}
	for _, r := range roots {
		sortTree(r)
	}
	sort.SliceStable(roots, func(a, b int) bool { return roots[a].date.Before(roots[b].date) })

	var sb strings.Builder
	for _, r := range roots {
		sb.WriteString("(")
		sb.WriteString(c.renderThread(r, byUID))
		sb.WriteString(")")
	}
	return sb.String()
}

// renderThread renders a thread-list body (RFC 5256): a linear chain of single
// children is space-separated; a branch point emits each child as its own
// parenthesized sub-thread.
func (c *conn) renderThread(n *tnode, byUID bool) string {
	members := []string{fmt.Sprintf("%d", c.threadID(n.idx, byUID))}
	cur := n
	for len(cur.children) == 1 {
		cur = cur.children[0]
		members = append(members, fmt.Sprintf("%d", c.threadID(cur.idx, byUID)))
	}
	content := strings.Join(members, " ")
	if len(cur.children) > 1 {
		content += " "
		for _, ch := range cur.children {
			content += "(" + c.renderThread(ch, byUID) + ")"
		}
	}
	return content
}

// firstAngle returns the first <...> token in a header, or "".
func firstAngle(s string) string {
	i := strings.IndexByte(s, '<')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i:], '>')
	if j < 0 {
		return ""
	}
	return s[i : i+j+1]
}

// allAngles returns every <...> token in a header, in order.
func allAngles(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			break
		}
		out = append(out, s[i:i+j+1])
		s = s[i+j+1:]
	}
	return out
}
