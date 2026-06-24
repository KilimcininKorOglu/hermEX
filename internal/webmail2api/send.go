package webmail2api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// mailAttachment is the SPA's MailAttachment (filename, content type, base64 body).
type mailAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

// sendRequest is the subset of the SPA's SendMailRequest the send handler honors.
type sendRequest struct {
	To                 []string         `json:"to"`
	Cc                 []string         `json:"cc"`
	Bcc                []string         `json:"bcc"`
	Subject            string           `json:"subject"`
	Body               string           `json:"body"`
	IsHTML             bool             `json:"is_html"`
	RequestReadReceipt bool             `json:"requestReadReceipt"`
	Importance         string           `json:"importance"`
	Attachments        []mailAttachment `json:"attachments"`
	SendAt             string           `json:"sendAt"`
	SignMessage        bool             `json:"signMessage"`    // server-mode S/MIME sign
	EncryptMessage     bool             `json:"encryptMessage"` // server-mode S/MIME encrypt
}

// decodeAttachment decodes an attachment body, accepting raw base64 or a data URL.
func decodeAttachment(content string) ([]byte, error) {
	if i := strings.Index(content, "base64,"); i >= 0 {
		content = content[i+len("base64,"):]
	}
	return base64.StdEncoding.DecodeString(content)
}

// handleMailSend builds the message through oxcmail.Export (the one proven path,
// never hand-rolled MIME), delivers it via the shared relay, and files a Sent
// copy. The From is the authenticated user, so the sender cannot be spoofed.
func (s *Server) handleMailSend(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	recipients := collectRecipients(req.To, req.Cc, req.Bcc)
	if len(recipients) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one recipient is required"})
		return
	}

	raw, err := s.buildOutgoing(c.Email, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not build the message"})
		return
	}

	// Server-mode S/MIME: sign/encrypt here with the server-held key. Browser-mode
	// users do this in the browser and use /mail/send-raw, so they do not set these.
	if req.SignMessage || req.EncryptMessage {
		signed, aerr := s.applySmime(c.Mailbox, raw, recipients, req.SignMessage, req.EncryptMessage)
		if aerr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": aerr.Error()})
			return
		}
		raw = signed
	}

	// Scheduled (send-later): file the built message in the Outbox with a deferred
	// send time; the release worker delivers it when due.
	if req.SendAt != "" {
		st, err := objectstore.Open(c.Mailbox)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
			return
		}
		defer st.Close()
		if err := scheduleOutbox(st, raw, req.SendAt); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not schedule: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scheduled": true})
		return
	}

	if _, err := mta.DeliverAndRelay(s.accounts, s.spool, c.Email, recipients, raw, time.Now()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delivery failed: " + err.Error()})
		return
	}

	// File a Sent copy (best-effort; a delivered message is not lost if this fails).
	if st, err := objectstore.Open(c.Mailbox); err == nil {
		_, _ = st.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, time.Now(), objectstore.FlagSeen)
		st.Close()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// collectRecipients flattens To/Cc/Bcc into a deduplicated-by-position address
// list, parsing each entry as a mail address but keeping bare strings as-is.
func collectRecipients(groups ...[]string) []string {
	out := make([]string, 0)
	for _, group := range groups {
		for _, a := range group {
			if a = strings.TrimSpace(a); a == "" {
				continue
			}
			if parsed, err := mail.ParseAddress(a); err == nil {
				out = append(out, parsed.Address)
			} else {
				out = append(out, a)
			}
		}
	}
	return out
}

// handleMailBuild builds the outgoing MIME from the compose fields and returns it
// unsent (base64), so the SPA can S/MIME sign and/or encrypt it client-side before
// posting it back to /mail/send-raw. The private key never reaches the server.
func (s *Server) handleMailBuild(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	raw, err := s.buildOutgoing(c.Email, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not build the message"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"raw": base64.StdEncoding.EncodeToString(raw)})
}

// handleMailSendRaw relays a client-built (S/MIME signed and/or encrypted) raw
// message. The SPA supplies the recipients separately because an encrypted body
// cannot be parsed for them. A Sent copy of the exact bytes is filed.
func (s *Server) handleMailSendRaw(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req struct {
		Raw string   `json:"raw"`
		To  []string `json:"to"`
		Cc  []string `json:"cc"`
		Bcc []string `json:"bcc"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.Raw))
	if err != nil || len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid raw message"})
		return
	}
	recipients := collectRecipients(req.To, req.Cc, req.Bcc)
	if len(recipients) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one recipient is required"})
		return
	}
	if _, err := mta.DeliverAndRelay(s.accounts, s.spool, c.Email, recipients, raw, time.Now()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delivery failed: " + err.Error()})
		return
	}
	if st, err := objectstore.Open(c.Mailbox); err == nil {
		_, _ = st.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, time.Now(), objectstore.FlagSeen)
		st.Close()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMailDraft saves (or replaces) a draft in the Drafts folder and returns
// its id so the SPA's autosave can replace the same draft on the next save.
func (s *Server) handleMailDraft(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req struct {
		ID      string   `json:"id"`
		To      []string `json:"to"`
		Cc      []string `json:"cc"`
		Bcc     []string `json:"bcc"`
		Subject string   `json:"subject"`
		Body    string   `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	raw, err := s.buildOutgoing(c.Email, sendRequest{To: req.To, Cc: req.Cc, Bcc: req.Bcc, Subject: req.Subject, Body: req.Body})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not build the draft"})
		return
	}
	st, err := objectstore.Open(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mailbox unavailable"})
		return
	}
	defer st.Close()
	// Replace the previous draft so autosave does not accumulate copies.
	if folder, uid, ok := parseMessageID(req.ID); ok && folder == "drafts" {
		_ = st.DeleteMessage(int64(mapi.PrivateFIDDraft), uid)
	}
	info, err := st.AppendMessage(int64(mapi.PrivateFIDDraft), raw, time.Now(), objectstore.FlagSeen)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save the draft"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": messageID("drafts", info.UID)})
}

// buildOutgoing maps the send fields onto a MAPI message and exports it to RFC
// 5322 bytes via oxcmail — mirroring the server-rendered webmail's compose path.
func (s *Server) buildOutgoing(from string, req sendRequest) ([]byte, error) {
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.Note")
	props.Set(mapi.PrSentRepresentingSmtpAddress, from)
	props.Set(mapi.PrSentRepresentingEmailAddress, from)
	props.Set(mapi.PrSentRepresentingAddrType, "SMTP")
	props.Set(mapi.PrSubject, req.Subject)
	props.Set(mapi.PrClientSubmitTime, mapi.UnixToNTTime(time.Now()))
	props.Set(mapi.PrInternetMessageID, "<"+randomHex()+"@"+s.hostname+">")
	switch req.Importance {
	case "high":
		props.Set(mapi.PrImportance, int32(mapi.ImportanceHigh))
	case "low":
		props.Set(mapi.PrImportance, int32(mapi.ImportanceLow))
	}
	if req.RequestReadReceipt {
		props.Set(mapi.PrReadReceiptRequested, true)
	}
	// The SPA sends a single body; is_html marks it HTML. Export needs the plain
	// part too (the text/plain alternative), derived by stripping tags.
	if req.IsHTML {
		props.Set(mapi.PrHTML, []byte(toCRLF(req.Body)))
		props.Set(mapi.PrBody, toCRLF(stripTags(req.Body)))
	} else {
		props.Set(mapi.PrBody, toCRLF(req.Body))
	}

	msg := &oxcmail.Message{Props: props}
	msg.Recipients = append(rcptBags(req.To, mapi.RecipTo), rcptBags(req.Cc, mapi.RecipCc)...)
	for _, a := range req.Attachments {
		data, err := decodeAttachment(a.Content)
		if err != nil {
			continue
		}
		var p mapi.PropertyValues
		p.Set(mapi.PrAttachMethod, int32(mapi.AttachByValue))
		p.Set(mapi.PrAttachDataBin, data)
		if a.ContentType != "" {
			p.Set(mapi.PrAttachMimeTag, a.ContentType)
		}
		if a.Filename != "" {
			p.Set(mapi.PrAttachLongFilename, a.Filename)
		}
		msg.Attachments = append(msg.Attachments, oxcmail.Attachment{Props: p})
	}
	return oxcmail.Export(msg, oxcmail.Options{})
}

// rcptBags builds the per-recipient MAPI property bags for a To/Cc field.
func rcptBags(addrs []string, rcptType int32) []mapi.PropertyValues {
	var bags []mapi.PropertyValues
	for _, a := range addrs {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		name, addr := "", a
		if parsed, err := mail.ParseAddress(a); err == nil {
			name, addr = parsed.Name, parsed.Address
		}
		var bag mapi.PropertyValues
		bag.Set(mapi.PrRecipientType, rcptType)
		bag.Set(mapi.PrAddrType, "SMTP")
		bag.Set(mapi.PrEmailAddress, addr)
		bag.Set(mapi.PrSmtpAddress, addr)
		if name != "" {
			bag.Set(mapi.PrDisplayName, name)
		}
		bags = append(bags, bag)
	}
	return bags
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// stripTags removes HTML tags for a crude text/plain alternative of an HTML body.
func stripTags(s string) string {
	return strings.TrimSpace(tagRE.ReplaceAllString(s, ""))
}

// toCRLF normalizes line endings to CRLF for the wire/store.
func toCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

// scheduleOutbox files a built message in the Outbox with a deferred send time
// (RFC3339), marked unsent, so the release worker delivers it when due.
func scheduleOutbox(st *objectstore.Store, raw []byte, sendAt string) error {
	when, err := time.Parse(time.RFC3339, sendAt)
	if err != nil {
		return err
	}
	info, err := st.AppendMessage(int64(mapi.PrivateFIDOutbox), raw, time.Now(), objectstore.FlagSeen)
	if err != nil {
		return err
	}
	return st.SetMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrDeferredSendTime, Value: mapi.UnixToNTTime(when)},
		{Tag: mapi.PrMessageFlags, Value: int32(mapi.MsgFlagUnsent)},
	})
}

// randomHex returns a short random hex token for a Message-ID local part.
func randomHex() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
