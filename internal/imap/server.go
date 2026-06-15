package imap

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// capabilities is the untagged CAPABILITY list. LITERAL+ is advertised because
// the lexer accepts non-synchronizing literals; AUTH=PLAIN because the server
// implements the SASL PLAIN mechanism.
const capabilities = "IMAP4rev1 LITERAL+ AUTH=PLAIN"

// connState is the IMAP connection state (RFC 3501 §3).
type connState int

const (
	stateNotAuth connState = iota
	stateAuth
	stateSelected
	stateLogout
)

// Server accepts IMAP connections and serves mailboxes resolved via Auth.
type Server struct {
	Auth      directory.Authenticator
	Hostname  string
	TLSConfig *tls.Config // when non-nil, advertise and accept STARTTLS
}

// Serve accepts connections on l until it is closed.
func (s *Server) Serve(l net.Listener) error {
	for {
		nc, err := l.Accept()
		if err != nil {
			return err
		}
		go s.handle(nc)
	}
}

func (s *Server) handle(nc net.Conn) {
	c := &conn{srv: s, bw: bufio.NewWriter(nc), state: stateNotAuth, nc: nc}
	c.rd = &commandReader{br: bufio.NewReader(nc), bw: c.bw}
	if _, ok := nc.(*tls.Conn); ok {
		c.isTLS = true
	}
	defer func() { c.nc.Close() }() // closes the upgraded conn after a STARTTLS swap
	defer func() {
		if c.st != nil {
			c.st.Close()
		}
	}()

	c.untagged("OK [CAPABILITY %s] hermEX IMAP4rev1 ready", c.caps())
	c.flush()

	for c.state != stateLogout {
		toks, err := c.rd.readCommand()
		if err != nil {
			return // connection closed or unreadable
		}
		c.dispatch(toks)
	}
}

// conn is one IMAP client connection.
type conn struct {
	srv      *Server
	nc       net.Conn // underlying connection, swapped for the TLS conn on STARTTLS
	rd       *commandReader
	bw       *bufio.Writer
	state    connState
	user     string
	st       *objectstore.Store
	sel      *selectedMailbox
	readOnly bool
	isTLS    bool
}

// caps returns the CAPABILITY list for this connection's current state. STARTTLS
// is advertised only when the server has a TLS config and the link is not
// already encrypted (RFC 3501 §6.2.1).
func (c *conn) caps() string {
	if c.srv.TLSConfig != nil && !c.isTLS {
		return capabilities + " STARTTLS"
	}
	return capabilities
}

// dispatch routes one lexed command to its handler.
func (c *conn) dispatch(toks []token) {
	if len(toks) == 0 {
		return // empty line; ignore
	}
	tag, ok := toks[0].str()
	if !ok {
		c.bad("*", "missing tag")
		return
	}
	if len(toks) < 2 {
		c.bad(tag, "missing command")
		return
	}
	name, _ := toks[1].str()
	args := toks[2:]

	switch strings.ToUpper(name) {
	case "CAPABILITY":
		c.untagged("CAPABILITY %s", c.caps())
		c.ok(tag, "CAPABILITY completed")
	case "STARTTLS":
		c.cmdStartTLS(tag)
	case "NOOP":
		c.poll()
		c.ok(tag, "NOOP completed")
	case "LOGOUT":
		c.untagged("BYE hermEX IMAP logging out")
		c.state = stateLogout
		c.ok(tag, "LOGOUT completed")
	case "LOGIN":
		c.cmdLogin(tag, args)
	case "AUTHENTICATE":
		c.cmdAuthenticate(tag, args)
	case "SELECT":
		c.cmdSelect(tag, args, false)
	case "EXAMINE":
		c.cmdSelect(tag, args, true)
	case "LIST":
		c.cmdList(tag, args, false)
	case "LSUB":
		c.cmdList(tag, args, true)
	case "STATUS":
		c.cmdStatus(tag, args)
	case "CREATE":
		c.cmdCreate(tag, args)
	case "DELETE":
		c.cmdDelete(tag, args)
	case "RENAME":
		c.cmdRename(tag, args)
	case "SUBSCRIBE":
		c.cmdSubscribe(tag, args, true)
	case "UNSUBSCRIBE":
		c.cmdSubscribe(tag, args, false)
	case "CHECK":
		if c.state != stateSelected {
			c.no(tag, "no mailbox selected")
			return
		}
		c.poll()
		c.ok(tag, "CHECK completed")
	case "FETCH":
		c.cmdFetch(tag, args, false)
	case "STORE":
		c.cmdStore(tag, args, false)
	case "SEARCH":
		c.cmdSearch(tag, args, false)
	case "COPY":
		c.cmdCopy(tag, args, false)
	case "APPEND":
		c.cmdAppend(tag, args)
	case "EXPUNGE":
		c.cmdExpunge(tag)
	case "CLOSE":
		c.cmdClose(tag)
	case "UID":
		c.cmdUID(tag, args)
	default:
		c.bad(tag, "unknown or unimplemented command")
	}
}

// --- authentication ---

func (c *conn) cmdLogin(tag string, args []token) {
	if c.state != stateNotAuth {
		c.no(tag, "already authenticated")
		return
	}
	if len(args) < 2 {
		c.bad(tag, "LOGIN requires a username and password")
		return
	}
	user, _ := args[0].str()
	pass, _ := args[1].str()
	c.finishAuth(tag, user, pass)
}

func (c *conn) cmdAuthenticate(tag string, args []token) {
	if c.state != stateNotAuth {
		c.no(tag, "already authenticated")
		return
	}
	if len(args) < 1 || !args[0].isAtom("PLAIN") {
		c.no(tag, "unsupported authentication mechanism")
		return
	}
	var resp string
	if len(args) >= 2 {
		resp, _ = args[1].str() // initial response
	} else {
		c.bw.WriteString("+ \r\n")
		c.flush()
		line, err := c.rd.readLine()
		if err != nil {
			return
		}
		if line == "*" {
			c.bad(tag, "authentication cancelled")
			return
		}
		resp = line
	}
	user, pass, ok := decodeSASLPlain(resp)
	if !ok {
		c.bad(tag, "invalid SASL response")
		return
	}
	c.finishAuth(tag, user, pass)
}

// decodeSASLPlain decodes a SASL PLAIN response: base64 of
// authzid NUL authcid NUL passwd. The authcid (login name) and passwd are
// returned; an absent authzid is allowed.
func decodeSASLPlain(b64 string) (user, pass string, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// finishAuth validates credentials and, on success, opens the user's store and
// enters the authenticated state.
func (c *conn) finishAuth(tag, user, pass string) {
	path, ok := c.srv.Auth.Authenticate(user, pass)
	if !ok {
		c.no(tag, "[AUTHENTICATIONFAILED] invalid credentials")
		return
	}
	st, err := objectstore.Open(path)
	if err != nil {
		c.no(tag, "mailbox unavailable")
		return
	}
	c.st = st
	c.user = user
	c.state = stateAuth
	c.ok(tag, "[CAPABILITY "+c.caps()+"] LOGIN completed")
}

// cmdStartTLS upgrades the connection to TLS in place (RFC 3501 §6.2.1). It is
// valid only before login and only once. Before replying it verifies the
// command reader holds no buffered data: any bytes pipelined behind STARTTLS
// would be plaintext smuggled across the TLS boundary (the CVE-2011-0411
// plaintext-injection class), so their presence tears the connection down. The
// TLS handshake runs over the raw connection and a fresh reader/writer is built
// over the TLS conn, so no pre-TLS buffered byte can survive into the secure
// session.
func (c *conn) cmdStartTLS(tag string) {
	if c.srv.TLSConfig == nil {
		c.no(tag, "STARTTLS not available")
		return
	}
	if c.isTLS {
		c.bad(tag, "TLS already active")
		return
	}
	if c.state != stateNotAuth {
		c.bad(tag, "STARTTLS only allowed before login")
		return
	}
	if c.rd.br.Buffered() > 0 {
		// Pipelined plaintext behind STARTTLS (injection attempt): end the
		// session without replying so the smuggled command never runs. Setting
		// stateLogout breaks the dispatch loop — a bare return here would only
		// leave cmdStartTLS and let handle read the injected command next.
		c.state = stateLogout
		return
	}
	c.ok(tag, "Begin TLS negotiation now")

	tc := tls.Server(c.nc, c.srv.TLSConfig)
	if err := tc.Handshake(); err != nil {
		c.state = stateLogout // handshake failed; end the session
		return
	}
	c.nc = tc
	c.bw = bufio.NewWriter(tc)
	c.rd = &commandReader{br: bufio.NewReader(tc), bw: c.bw}
	c.isTLS = true
}

// --- mailbox selection ---

func (c *conn) cmdSelect(tag string, args []token, examine bool) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	name, ok := arg0(args)
	if !ok {
		c.bad(tag, "SELECT requires a mailbox name")
		return
	}
	tree, err := loadFolderTree(c.st)
	if err != nil {
		c.no(tag, "cannot read mailbox list")
		return
	}
	node, found := tree.resolve(name)
	if !found {
		c.state = stateAuth // a failed SELECT deselects any current mailbox
		c.sel = nil
		c.no(tag, "no such mailbox")
		return
	}
	sel, err := loadMailbox(c.st, node.info.ID, node.path)
	if err != nil {
		c.no(tag, "cannot open mailbox")
		return
	}
	c.sel = sel
	c.state = stateSelected
	c.readOnly = examine

	c.untagged("%d EXISTS", sel.maxSeq())
	c.untagged("0 RECENT")
	c.untagged(`FLAGS (%s)`, supportedFlagNames())
	if examine {
		c.untagged(`OK [PERMANENTFLAGS ()] read-only, no permanent flags`)
	} else {
		c.untagged(`OK [PERMANENTFLAGS (%s)] limited`, supportedFlagNames())
	}
	c.untagged("OK [UIDVALIDITY %d] validity", sel.uidValidity)
	c.untagged("OK [UIDNEXT %d] next uid", sel.uidNext)
	if u := sel.firstUnseen(); u != 0 {
		c.untagged("OK [UNSEEN %d] first unseen", u)
	}
	if examine {
		c.ok(tag, "[READ-ONLY] EXAMINE completed")
	} else {
		c.ok(tag, "[READ-WRITE] SELECT completed")
	}
}

// --- listing ---

func (c *conn) cmdList(tag string, args []token, lsub bool) {
	verb := "LIST"
	if lsub {
		verb = "LSUB"
	}
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	ref, ok1 := arg0(args)
	pat, ok2 := argN(args, 1)
	if !ok1 || !ok2 {
		c.bad(tag, verb+" requires a reference and a pattern")
		return
	}
	// An empty pattern is a request for the hierarchy delimiter and root name.
	if pat == "" {
		c.untagged(`%s (\Noselect) "%s" ""`, verb, hierarchySep)
		c.ok(tag, verb+" completed")
		return
	}
	tree, err := loadFolderTree(c.st)
	if err != nil {
		c.no(tag, "cannot read mailbox list")
		return
	}
	full := ref + pat
	for _, n := range tree.nodes {
		if lsub && !n.info.Subscribed {
			continue
		}
		if !imapMatch(full, n.path) {
			continue
		}
		attr := `\HasNoChildren`
		if n.hasChildren {
			attr = `\HasChildren`
		}
		c.untagged(`%s (%s) "%s" %s`, verb, attr, hierarchySep, quoteString(n.path))
	}
	c.ok(tag, verb+" completed")
}

// --- status ---

func (c *conn) cmdStatus(tag string, args []token) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	name, ok := arg0(args)
	if !ok || len(args) < 2 || args[1].kind != tLParen {
		c.bad(tag, "STATUS requires a mailbox and item list")
		return
	}
	items := parenAtoms(args[1:])
	tree, err := loadFolderTree(c.st)
	if err != nil {
		c.no(tag, "cannot read mailbox list")
		return
	}
	node, found := tree.resolve(name)
	if !found {
		c.no(tag, "no such mailbox")
		return
	}
	msgs, err := c.st.ListMessages(node.info.ID)
	if err != nil {
		c.no(tag, "cannot read mailbox")
		return
	}
	uidv, _ := c.st.UIDValidity(node.info.ID)
	uidn, _ := c.st.UIDNext(node.info.ID)
	unseen := 0
	for i := range msgs {
		if msgs[i].Flags&objectstore.FlagSeen == 0 {
			unseen++
		}
	}
	var parts []string
	for _, it := range items {
		switch strings.ToUpper(it) {
		case "MESSAGES":
			parts = append(parts, fmt.Sprintf("MESSAGES %d", len(msgs)))
		case "RECENT":
			parts = append(parts, "RECENT 0")
		case "UIDNEXT":
			parts = append(parts, fmt.Sprintf("UIDNEXT %d", uidn))
		case "UIDVALIDITY":
			parts = append(parts, fmt.Sprintf("UIDVALIDITY %d", uidv))
		case "UNSEEN":
			parts = append(parts, fmt.Sprintf("UNSEEN %d", unseen))
		}
	}
	c.untagged("STATUS %s (%s)", quoteString(node.path), strings.Join(parts, " "))
	c.ok(tag, "STATUS completed")
}

// --- folder management ---

func (c *conn) cmdCreate(tag string, args []token) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	name, ok := arg0(args)
	if !ok || name == "" {
		c.bad(tag, "CREATE requires a mailbox name")
		return
	}
	// A trailing hierarchy separator (declaring intent to hold children) is
	// stripped; the folder itself is what gets created.
	name = strings.TrimSuffix(name, hierarchySep)
	if strings.EqualFold(name, inboxName) {
		c.no(tag, "INBOX already exists")
		return
	}
	tree, err := loadFolderTree(c.st)
	if err != nil {
		c.no(tag, "cannot read mailbox list")
		return
	}
	if _, exists := tree.resolve(name); exists {
		c.no(tag, "mailbox already exists")
		return
	}
	if _, err := c.createPath(tree, name); err != nil {
		c.no(tag, "cannot create mailbox")
		return
	}
	c.ok(tag, "CREATE completed")
}

// createPath creates a mailbox path, creating any missing intermediate folders,
// and returns the leaf folder id.
func (c *conn) createPath(tree *folderTree, path string) (int64, error) {
	parts := strings.Split(path, hierarchySep)
	var parent *int64
	prefix := ""
	for i, part := range parts {
		if i == 0 {
			prefix = part
		} else {
			prefix += hierarchySep + part
		}
		if n, ok := tree.resolve(prefix); ok {
			id := n.info.ID
			parent = &id
			continue
		}
		id, err := c.st.CreateFolder(parent, part)
		if err != nil {
			return 0, err
		}
		// Refresh the tree so subsequent resolve() calls see the new folder.
		if tree, err = loadFolderTree(c.st); err != nil {
			return 0, err
		}
		parent = &id
	}
	if parent == nil {
		return 0, fmt.Errorf("imap: empty mailbox path")
	}
	return *parent, nil
}

func (c *conn) cmdDelete(tag string, args []token) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	name, ok := arg0(args)
	if !ok {
		c.bad(tag, "DELETE requires a mailbox name")
		return
	}
	if strings.EqualFold(name, inboxName) {
		c.no(tag, "cannot delete INBOX")
		return
	}
	tree, err := loadFolderTree(c.st)
	if err != nil {
		c.no(tag, "cannot read mailbox list")
		return
	}
	node, found := tree.resolve(name)
	if !found {
		c.no(tag, "no such mailbox")
		return
	}
	if err := c.st.DeleteFolder(node.info.ID); err != nil {
		c.no(tag, "cannot delete mailbox")
		return
	}
	c.ok(tag, "DELETE completed")
}

func (c *conn) cmdRename(tag string, args []token) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	from, ok1 := arg0(args)
	to, ok2 := argN(args, 1)
	if !ok1 || !ok2 || to == "" {
		c.bad(tag, "RENAME requires source and destination names")
		return
	}
	if strings.EqualFold(from, inboxName) {
		c.no(tag, "renaming INBOX is not supported")
		return
	}
	tree, err := loadFolderTree(c.st)
	if err != nil {
		c.no(tag, "cannot read mailbox list")
		return
	}
	node, found := tree.resolve(from)
	if !found {
		c.no(tag, "no such mailbox")
		return
	}
	if _, exists := tree.resolve(to); exists {
		c.no(tag, "destination already exists")
		return
	}
	to = strings.TrimSuffix(to, hierarchySep)
	parent, leaf, err := c.resolveParent(tree, to)
	if err != nil {
		c.no(tag, "cannot create destination parent")
		return
	}
	if err := c.st.RenameFolder(node.info.ID, parent, leaf); err != nil {
		c.no(tag, "cannot rename mailbox")
		return
	}
	c.ok(tag, "RENAME completed")
}

// resolveParent splits a mailbox path into its parent folder id (nil for root)
// and leaf display name, creating missing intermediate folders.
func (c *conn) resolveParent(tree *folderTree, path string) (parent *int64, leaf string, err error) {
	idx := strings.LastIndex(path, hierarchySep)
	if idx < 0 {
		return nil, path, nil
	}
	parentPath := path[:idx]
	leaf = path[idx+len(hierarchySep):]
	id, err := c.createPath(tree, parentPath)
	if err != nil {
		return nil, "", err
	}
	return &id, leaf, nil
}

func (c *conn) cmdSubscribe(tag string, args []token, subscribe bool) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	name, ok := arg0(args)
	if !ok {
		c.bad(tag, "SUBSCRIBE requires a mailbox name")
		return
	}
	tree, err := loadFolderTree(c.st)
	if err != nil {
		c.no(tag, "cannot read mailbox list")
		return
	}
	node, found := tree.resolve(name)
	if !found {
		c.no(tag, "no such mailbox")
		return
	}
	if err := c.st.SetSubscribed(node.info.ID, subscribe); err != nil {
		c.no(tag, "cannot update subscription")
		return
	}
	c.ok(tag, "completed")
}

// --- mailbox polling (untagged updates) ---

// poll re-reads the selected folder and emits untagged EXPUNGE responses for
// vanished messages and an EXISTS response when new messages have arrived. It
// is a no-op outside the selected state.
func (c *conn) poll() {
	if c.state != stateSelected || c.sel == nil {
		return
	}
	fresh, err := loadMailbox(c.st, c.sel.id, c.sel.path)
	if err != nil {
		return
	}
	live := make(map[uint32]bool, len(fresh.msgs))
	for i := range fresh.msgs {
		live[fresh.msgs[i].UID] = true
	}
	// Emit EXPUNGE for each vanished UID. Sequence numbers are reported against
	// the shrinking view, so removing index i renumbers everything after it.
	view := make([]objectstore.MessageInfo, len(c.sel.msgs))
	copy(view, c.sel.msgs)
	for i := 0; i < len(view); {
		if live[view[i].UID] {
			i++
			continue
		}
		c.untagged("%d EXPUNGE", i+1)
		view = append(view[:i], view[i+1:]...)
	}
	if len(fresh.msgs) != len(view) {
		c.untagged("%d EXISTS", fresh.maxSeq())
		c.untagged("0 RECENT")
	}
	c.sel = fresh
}

// --- argument helpers ---

// arg0 returns the textual value of the first argument.
func arg0(args []token) (string, bool) {
	return argN(args, 0)
}

// argN returns the textual value of the n-th argument (atom or string).
func argN(args []token, n int) (string, bool) {
	if n >= len(args) {
		return "", false
	}
	return args[n].str()
}

// parenAtoms returns the atom values inside a parenthesized list starting at
// args[0] == '('. It stops at the matching ')'.
func parenAtoms(args []token) []string {
	if len(args) == 0 || args[0].kind != tLParen {
		return nil
	}
	var out []string
	for _, t := range args[1:] {
		if t.kind == tRParen {
			break
		}
		if s, ok := t.str(); ok {
			out = append(out, s)
		}
	}
	return out
}

// quoteString renders s as an IMAP quoted string, escaping the quote and
// backslash characters.
func quoteString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// --- response writers ---

func (c *conn) untagged(format string, a ...any) {
	c.bw.WriteString("* ")
	fmt.Fprintf(c.bw, format, a...)
	c.bw.WriteString("\r\n")
}

func (c *conn) respond(tag, status, format string, a ...any) {
	fmt.Fprintf(c.bw, "%s %s ", tag, status)
	fmt.Fprintf(c.bw, format, a...)
	c.bw.WriteString("\r\n")
	c.flush()
}

func (c *conn) ok(tag, text string)  { c.respond(tag, "OK", "%s", text) }
func (c *conn) no(tag, text string)  { c.respond(tag, "NO", "%s", text) }
func (c *conn) bad(tag, text string) { c.respond(tag, "BAD", "%s", text) }

func (c *conn) flush() { c.bw.Flush() }
