package activesync

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// handleSendMail answers SendMail, SmartReply, and SmartForward. The device
// composes the complete message (the reply/forward already includes any quoted
// source), so all three reduce to: extract the MIME, deliver it to the
// recipients in its headers, and save a copy to Sent. Success is a bare HTTP 200
// with no body (MS-ASCMD). For a reply or forward, the source message is then
// marked replied/forwarded so the icon survives across devices.
func (s *Server) handleSendMail(w http.ResponseWriter, r *http.Request, sess *session) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyLimit()))
	if err != nil {
		s.failRequest(w, r, "sendmail.read.fail", err, http.StatusBadRequest, "cannot read request body")
		return
	}
	cm, err := extractComposeMail(body)
	if err != nil {
		s.failRequest(w, r, "sendmail.parse.fail", err, http.StatusBadRequest, "invalid SendMail")
		return
	}
	recipients := recipientsOf(cm.mime)
	if len(recipients) == 0 {
		http.Error(w, "SendMail has no recipients", http.StatusBadRequest)
		return
	}

	// The device composes the whole MIME, so the From header is client-controlled
	// and the DKIM signer keys on its domain. Authorize it against the caller the
	// same way every other send path does, and fail closed rather than relay a
	// DKIM-aligned message the caller may not send as.
	if !s.authorizedFrom(sess.user, cm.mime) {
		s.failRequest(w, r, "sendmail.sender.denied", errUnauthorizedFrom, http.StatusForbidden, "sender not authorized")
		return
	}

	// Deliver with Bcc stripped so recipients never see the blind list; the
	// saved copy keeps the full headers for the sender's record.
	if _, err := mta.DeliverAndRelay(s.accounts, s.Spool, sess.user, recipients, stripBcc(cm.mime), time.Now()); err != nil {
		s.failRequest(w, r, "sendmail.delivery.fail", err, http.StatusInternalServerError, "delivery failed")
		return
	}

	// A reply or forward references a source message in the sender's own mailbox,
	// in the WBXML Source or the URL query. Resolving it only against sess.mailbox
	// keeps the mark scoped to the authenticated identity (OWASP A01).
	forward := sess.req.cmd == "SmartForward"
	var srcFolder, srcItem string
	if forward || sess.req.cmd == "SmartReply" {
		srcFolder, srcItem = resolveSource(cm, r)
	}
	needMark := srcFolder != "" && srcItem != ""

	if cm.saveToSent || needMark {
		st, err := objectstore.Open(sess.mailbox)
		if err == nil {
			if cm.saveToSent {
				_, _ = st.AppendMessage(int64(mapi.PrivateFIDSentItems), cm.mime, time.Now(), objectstore.FlagSeen)
			}
			if needMark {
				markReplyForwardSource(st, srcFolder, srcItem, forward)
			}
			_ = st.Close()
		}
	}
	w.WriteHeader(http.StatusOK)
}

// errUnauthorizedFrom is returned when a SendMail message carries a From header
// the authenticated caller is not allowed to send as.
var errUnauthorizedFrom = errors.New("message From not authorized for caller")

// authorizedFrom reports whether the authenticated caller may put the message's
// From header on the wire. It fails closed like the MTA send-as gate
// (internal/mta/delivery.go): an absent or unparseable From is allowed (there is
// no identity to forge and delivery keeps the caller as sender), the caller's own
// address or an alias is allowed, and any other address is allowed only when the
// mailbox that owns it has granted the caller a send-as permission.
func (s *Server) authorizedFrom(caller string, mime []byte) bool {
	from, ok := parseFromAddress(mime)
	if !ok {
		return true
	}
	ids := s.callerIdentities(caller)
	want := strings.ToLower(strings.TrimSpace(from))
	if containsFold(ids, want) {
		return true
	}
	return s.grantedSendAs(want, ids)
}

// callerIdentities returns the caller's own address plus any directory aliases.
func (s *Server) callerIdentities(caller string) []string {
	ids := []string{caller}
	if idr, ok := s.accounts.(directory.Identifier); ok {
		if addrs, err := idr.Identities(caller); err == nil && len(addrs) > 0 {
			ids = addrs
		}
	}
	return ids
}

// grantedSendAs reports whether one of the caller's identities appears in the
// send-as list of the mailbox that owns from. It fails closed: an address that
// resolves to no local mailbox, a store that will not open, or an unreadable list
// denies the grant rather than risking a forged From.
func (s *Server) grantedSendAs(from string, ids []string) bool {
	path, ok := s.accounts.Resolve(from)
	if !ok {
		return false
	}
	st, err := objectstore.Open(path)
	if err != nil {
		return false
	}
	defer st.Close()
	list, err := st.GetSendAs()
	if err != nil {
		return false
	}
	for _, g := range list {
		if containsFold(ids, strings.ToLower(strings.TrimSpace(g))) {
			return true
		}
	}
	return false
}

// containsFold reports whether want (already lowercased) equals any address in
// list, compared case-insensitively after trimming.
func containsFold(list []string, want string) bool {
	for _, a := range list {
		if strings.ToLower(strings.TrimSpace(a)) == want {
			return true
		}
	}
	return false
}

// parseFromAddress extracts the bare address from a message's From header, or
// reports ok=false when there is no parseable From.
func parseFromAddress(mime []byte) (addr string, ok bool) {
	msg, err := mail.ReadMessage(bytes.NewReader(mime))
	if err != nil {
		return "", false
	}
	h := msg.Header.Get("From")
	if h == "" {
		return "", false
	}
	a, err := mail.ParseAddress(h)
	if err != nil {
		return "", false
	}
	return a.Address, true
}

// composeMail is a decoded ComposeMail request: the message MIME, whether to save
// a Sent copy, and the source-message identity a reply/forward references (all
// source fields empty for a plain SendMail).
type composeMail struct {
	mime       []byte
	saveToSent bool
	srcFolder  string
	srcItem    string
	srcLongID  string
}

// extractComposeMail decodes a ComposeMail request. EAS 14.x wraps the MIME in a
// WBXML envelope (carrying SaveInSentItems and, for a reply/forward, the Source);
// older clients POST raw MIME directly (detected by the absent WBXML version byte).
func extractComposeMail(body []byte) (composeMail, error) {
	if len(body) == 0 {
		return composeMail{}, io.ErrUnexpectedEOF
	}
	if body[0] != 0x03 { // not WBXML: legacy raw-MIME SendMail
		return composeMail{mime: body, saveToSent: true}, nil
	}
	root, err := wbxml.Unmarshal(body)
	if err != nil {
		return composeMail{}, err
	}
	m := root.Child(wbxml.CMMIME)
	if m == nil {
		return composeMail{}, io.ErrUnexpectedEOF
	}
	mimeData := m.Opaque
	if mimeData == nil {
		mimeData = []byte(m.Text)
	}
	cm := composeMail{mime: mimeData, saveToSent: root.Child(wbxml.CMSaveInSentItems) != nil}
	if src := root.Child(wbxml.CMSource); src != nil {
		cm.srcFolder = src.ChildText(wbxml.CMFolderID)
		cm.srcItem = src.ChildText(wbxml.CMItemID)
		cm.srcLongID = src.ChildText(wbxml.CMLongId)
	}
	return cm, nil
}

// resolveSource resolves the source message a reply/forward references, mirroring
// the reference precedence: the WBXML envelope's Source, then the URL query
// (ItemId/CollectionId, the only carrier for a legacy raw-MIME reply), and finally
// a LongId, which overrides both by splitting into folder and item on the colon
// the Search/Find result render joins them with.
func resolveSource(cm composeMail, r *http.Request) (folderID, itemID string) {
	folderID, itemID = cm.srcFolder, cm.srcItem
	q := r.URL.Query()
	if itemID == "" {
		itemID = q.Get("ItemId")
	}
	if folderID == "" {
		folderID = q.Get("CollectionId")
	}
	if cm.srcLongID != "" {
		if f, i, ok := strings.Cut(cm.srcLongID, ":"); ok {
			folderID, itemID = f, i
		}
	}
	return folderID, itemID
}

// markReplyForwardSource stamps the reply/forward verb on the source message so a
// client renders the replied/forwarded icon (PidTagLastVerbExecuted, MS-OXOMSG). A
// reply also sets the \Answered IMAP flag, the marker IMAP and webmail surface,
// merged into the current flags so the read state survives (SetMessageFlags
// rewrites the whole mask). Best-effort: a missing or unparseable source is
// silently skipped, never failing a send whose mail already left.
func markReplyForwardSource(st *objectstore.Store, folderID, itemID string, forward bool) {
	fid, err := strconv.ParseInt(folderID, 10, 64)
	if err != nil {
		return
	}
	uid64, err := strconv.ParseUint(itemID, 10, 32)
	if err != nil {
		return
	}
	uid := uint32(uid64)
	info, err := st.MessageByUID(fid, uid)
	if err != nil {
		return
	}
	verb := mapi.NoteVerbReplyToSender
	if forward {
		verb = mapi.NoteVerbForward
	}
	var props mapi.PropertyValues
	props.Set(mapi.PrLastVerbExecuted, verb)
	props.Set(mapi.PrLastVerbExecutionTime, mapi.UnixToNTTime(time.Now()))
	_ = st.ModifyMessageProperties(info.ID, props)
	if !forward {
		_ = st.SetMessageFlags(fid, uid, info.Flags|objectstore.FlagAnswered)
	}
}

// recipientsOf collects the To, Cc, and Bcc addresses from a message's headers.
func recipientsOf(raw []byte) []string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	var out []string
	for _, field := range []string{"To", "Cc", "Bcc"} {
		list, err := msg.Header.AddressList(field)
		if err != nil {
			continue
		}
		for _, a := range list {
			out = append(out, a.Address)
		}
	}
	return out
}

// stripBcc removes the Bcc header field, including any folded continuation
// lines, from a message's header block. It leaves the body untouched.
func stripBcc(raw []byte) []byte {
	sep := []byte("\r\n\r\n")
	idx := bytes.Index(raw, sep)
	if idx < 0 {
		sep = []byte("\n\n")
		idx = bytes.Index(raw, sep)
	}
	if idx < 0 {
		return raw // no header/body boundary; nothing safe to do
	}
	header, body := raw[:idx], raw[idx:]
	var kept [][]byte
	skipping := false
	for line := range bytes.SplitSeq(header, []byte("\n")) {
		trimmed := bytes.TrimRight(line, "\r")
		folded := len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t')
		if skipping {
			if folded {
				continue
			}
			skipping = false
		}
		if bytes.HasPrefix(bytes.ToLower(trimmed), []byte("bcc:")) {
			skipping = true
			continue
		}
		kept = append(kept, line)
	}
	return append(bytes.Join(kept, []byte("\n")), body...)
}
