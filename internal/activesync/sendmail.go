package activesync

import (
	"bytes"
	"io"
	"net/http"
	"net/mail"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// handleSendMail answers SendMail, SmartReply, and SmartForward. The device
// composes the complete message (the reply/forward already includes any quoted
// source), so all three reduce to: extract the MIME, deliver it to the
// recipients in its headers, and save a copy to Sent. Success is a bare HTTP 200
// with no body (MS-ASCMD). The source-message linkage SmartReply/SmartForward
// carry (CollectionId/ItemId, the answered/forwarded flag) is v2.
func (s *Server) handleSendMail(w http.ResponseWriter, r *http.Request, sess *session) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyLimit()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	message, saveToSent, err := extractComposeMail(body)
	if err != nil {
		http.Error(w, "invalid SendMail: "+err.Error(), http.StatusBadRequest)
		return
	}
	recipients := recipientsOf(message)
	if len(recipients) == 0 {
		http.Error(w, "SendMail has no recipients", http.StatusBadRequest)
		return
	}

	// Deliver with Bcc stripped so recipients never see the blind list; the
	// saved copy keeps the full headers for the sender's record.
	if _, err := mta.DeliverAndRelay(s.accounts, s.Spool, sess.user, recipients, stripBcc(message), time.Now()); err != nil {
		http.Error(w, "delivery failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if saveToSent {
		st, err := objectstore.Open(sess.mailbox)
		if err == nil {
			_, _ = st.AppendMessage(int64(mapi.PrivateFIDSentItems), message, time.Now(), objectstore.FlagSeen)
			st.Close()
		}
	}
	w.WriteHeader(http.StatusOK)
}

// extractComposeMail returns the message MIME and whether to save a Sent copy.
// EAS 14.x wraps the MIME in a ComposeMail WBXML envelope; older clients POST
// raw MIME directly (detected by the absence of the WBXML version byte).
func extractComposeMail(body []byte) (message []byte, saveToSent bool, err error) {
	if len(body) == 0 {
		return nil, false, io.ErrUnexpectedEOF
	}
	if body[0] != 0x03 { // not WBXML: legacy raw-MIME SendMail
		return body, true, nil
	}
	root, err := wbxml.Unmarshal(body)
	if err != nil {
		return nil, false, err
	}
	m := root.Child(wbxml.CMMIME)
	if m == nil {
		return nil, false, io.ErrUnexpectedEOF
	}
	mimeData := m.Opaque
	if mimeData == nil {
		mimeData = []byte(m.Text)
	}
	return mimeData, root.Child(wbxml.CMSaveInSentItems) != nil, nil
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
