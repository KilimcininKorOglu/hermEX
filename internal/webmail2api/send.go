package webmail2api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
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
	To                     []string         `json:"to"`
	Cc                     []string         `json:"cc"`
	Bcc                    []string         `json:"bcc"`
	Subject                string           `json:"subject"`
	Body                   string           `json:"body"`
	IsHTML                 bool             `json:"is_html"`
	RequestReadReceipt     bool             `json:"requestReadReceipt"`
	RequestDeliveryReceipt bool             `json:"requestDeliveryReceipt"` // PR_ORIGINATOR_DELIVERY_REPORT_REQUESTED
	Importance             string           `json:"importance"`
	Sensitivity            string           `json:"sensitivity"` // "personal"|"private"|"confidential" → PR_SENSITIVITY
	Attachments            []mailAttachment `json:"attachments"`
	SendAt                 string           `json:"sendAt"`
	SignMessage            bool             `json:"signMessage"`    // server-mode S/MIME sign
	EncryptMessage         bool             `json:"encryptMessage"` // server-mode S/MIME encrypt
	From                   string           `json:"from"`           // chosen sender identity (send-as / on-behalf); empty = self
}

// badAttachmentError reports an attachment whose body could not be decoded. It is
// a fault in what the client sent, not in the server, so the send paths answer it
// with a client error naming the file rather than a generic failure.
type badAttachmentError struct{ name string }

func (e badAttachmentError) Error() string { return "attachment could not be decoded: " + e.name }

// writeBuildError answers a failed message build. An attachment the client sent
// that will not decode is the client's fault and names the file so it can be
// corrected; anything else is an internal failure and stays generic.
func writeBuildError(w http.ResponseWriter, err error, generic string) {
	var bad badAttachmentError
	if errors.As(err, &bad) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read attachment " + bad.name})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": generic})
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
	if bad, found := firstUnusableAddress(req.To, req.Cc, req.Bcc); found {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a valid email address: " + bad})
		return
	}
	recipients := collectRecipients(req.To, req.Cc, req.Bcc)
	if len(recipients) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one recipient is required"})
		return
	}

	// Authorize the chosen From identity (fail-closed). The envelope sender stays
	// the authenticated caller so bounces return to them and the relay's own
	// send-as gate always passes; only the header From/Sender reflect the identity.
	representing, sender, ok := s.resolveSender(c.Email, req.From)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you may not send as this address"})
		return
	}

	raw, err := s.buildOutgoing(representing, sender, req)
	if err != nil {
		writeBuildError(w, err, "could not build the message")
		return
	}

	// Server-mode S/MIME: sign/encrypt here with the server-held key. Browser-mode
	// users do this in the browser and use /mail/send-raw, so they do not set these.
	if req.SignMessage || req.EncryptMessage {
		signed, aerr := s.applySmime(c.Mailbox, raw, recipients, req.SignMessage, req.EncryptMessage)
		if aerr != nil {
			// The wrapped error names the signing library's internals and, on the
			// encrypt path, a recipient's certificate state. The caller is told what
			// failed, not how.
			logError("smime-apply", aerr, logging.Fields{"user": c.Email})
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not sign or encrypt the message"})
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
			logError("schedule-send", err, logging.Fields{"user": c.Email})
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not schedule the message"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scheduled": true})
		return
	}

	if _, err := mta.DeliverAndRelay(s.accounts, s.spool, c.Email, recipients, raw, time.Now()); err != nil {
		// Delivery errors carry mailbox filesystem paths and database driver text;
		// they belong in the log, not in a response to the browser.
		logError("send-mail", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delivery failed"})
		return
	}

	// File a Sent copy (best-effort; a delivered message is not lost if this fails).
	if st, err := objectstore.Open(c.Mailbox); err == nil {
		fileSentCopy(st, raw, c.Email, "mail")
		st.Close()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// firstUnusableAddress returns the first entry that cannot be parsed as an
// address. Such a string must never be carried into the message: mail.Address's
// String() escapes the local part but concatenates the domain verbatim, so an
// entry holding a line break would splice headers of the sender's choosing into
// the outgoing message (or end the header block and push the rest into the body).
func firstUnusableAddress(groups ...[]string) (string, bool) {
	for _, group := range groups {
		for _, a := range group {
			if a = strings.TrimSpace(a); a == "" {
				continue
			}
			if _, err := mail.ParseAddress(a); err != nil {
				return a, true
			}
		}
	}
	return "", false
}

// collectRecipients flattens To/Cc/Bcc into a deduplicated-by-position address
// list. An entry that does not parse as an address is dropped rather than passed
// through raw: it cannot be delivered anyway, and the handlers reject one before
// reaching here.
func collectRecipients(groups ...[]string) []string {
	out := make([]string, 0)
	for _, group := range groups {
		for _, a := range group {
			if a = strings.TrimSpace(a); a == "" {
				continue
			}
			parsed, err := mail.ParseAddress(a)
			if err != nil {
				continue
			}
			out = append(out, parsed.Address)
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
	if bad, found := firstUnusableAddress(req.To, req.Cc, req.Bcc); found {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a valid email address: " + bad})
		return
	}
	representing, sender, ok := s.resolveSender(c.Email, req.From)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you may not send as this address"})
		return
	}
	raw, err := s.buildOutgoing(representing, sender, req)
	if err != nil {
		writeBuildError(w, err, "could not build the message")
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
	// The bytes are client-built, so the identity they assert has to clear the same
	// send-as gate the two building handlers apply. The message is never rewritten
	// here (that would break an S/MIME signature), so the From header is read out
	// and authorized as-is; the outbound signer keys DKIM off this same header, so
	// an unauthorized From would otherwise be relayed DMARC-aligned.
	from, ok := rawFromAddress(raw)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid raw message"})
		return
	}
	if _, _, allowed := s.resolveSender(c.Email, from); !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you may not send as this address"})
		return
	}
	if bad, found := firstUnusableAddress(req.To, req.Cc, req.Bcc); found {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a valid email address: " + bad})
		return
	}
	recipients := collectRecipients(req.To, req.Cc, req.Bcc)
	if len(recipients) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one recipient is required"})
		return
	}
	if _, err := mta.DeliverAndRelay(s.accounts, s.spool, c.Email, recipients, raw, time.Now()); err != nil {
		logError("send-raw", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delivery failed"})
		return
	}
	if st, err := objectstore.Open(c.Mailbox); err == nil {
		fileSentCopy(st, raw, c.Email, "mail-raw")
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
	if bad, found := firstUnusableAddress(req.To, req.Cc, req.Bcc); found {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a valid email address: " + bad})
		return
	}
	raw, err := s.buildOutgoing(c.Email, c.Email, sendRequest{To: req.To, Cc: req.Cc, Bcc: req.Bcc, Subject: req.Subject, Body: req.Body})
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

// rawFromAddress reads the single From identity a client-built raw message
// asserts. RFC 5322 permits a From list, but a compose never emits one and a
// second address is a way to hide the address that will actually be authorized,
// so exactly one is required; anything else is refused as malformed.
func rawFromAddress(raw []byte) (string, bool) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", false
	}
	list, err := mail.ParseAddressList(msg.Header.Get("From"))
	if err != nil || len(list) != 1 {
		return "", false
	}
	return list[0].Address, true
}

// resolveSender authorizes the caller's chosen From identity and returns the
// address to represent plus the real authenticated sender. It mirrors the MTA's
// send-as gate (internal/mta/delivery.go) exactly, because /mail/send never
// traverses the authenticated SMTP path where that gate runs: an empty or
// self-matching want sends as the caller; any other address is allowed only when
// it is one of the caller's directory identities (an alias) or when the mailbox
// that owns it has granted the caller a send-as permission. It fails closed,
// an unresolvable owner, an unopenable store, or an unreadable list denies the
// identity rather than risking a forged From. representing is the authorized
// From; sender is always the real caller so oxcmail emits a Sender header (RFC
// 5322 "on behalf of") whenever the two differ.
func (s *Server) resolveSender(caller, want string) (representing, sender string, ok bool) {
	want = strings.TrimSpace(want)
	if want == "" || strings.EqualFold(want, caller) {
		return caller, caller, true
	}
	// An alias of the caller: send as it directly, with no Sender header.
	if id, isID := s.accounts.(directory.Identifier); isID {
		if addrs, err := id.Identities(caller); err == nil {
			for _, a := range addrs {
				if strings.EqualFold(strings.TrimSpace(a), want) {
					return want, want, true
				}
			}
		}
	}
	// A send-as grant from the mailbox that owns want: represent that mailbox,
	// but keep the real caller in Sender so the recipient sees "caller on behalf".
	if s.grantedSendAs(caller, want) {
		return want, caller, true
	}
	return "", "", false
}

// grantedSendAs reports whether caller appears in the send-as list of the mailbox
// that owns want. It fails closed identically to the MTA gate: any resolution,
// open, or read failure denies the grant.
func (s *Server) grantedSendAs(caller, want string) bool {
	path, ok := s.accounts.Resolve(want)
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
	ids := []string{caller}
	if idr, isID := s.accounts.(directory.Identifier); isID {
		if addrs, aerr := idr.Identities(caller); aerr == nil && len(addrs) > 0 {
			ids = addrs
		}
	}
	for _, g := range list {
		g = strings.ToLower(strings.TrimSpace(g))
		for _, id := range ids {
			if strings.EqualFold(strings.TrimSpace(id), g) {
				return true
			}
		}
	}
	return false
}

// buildOutgoing maps the send fields onto a MAPI message and exports it to RFC
// 5322 bytes via oxcmail, mirroring the server-rendered webmail's compose path.
// representing is the authorized From identity; sender is the real authenticated
// caller. When they differ (send-on-behalf) oxcmail emits a Sender header.
func (s *Server) buildOutgoing(representing, sender string, req sendRequest) ([]byte, error) {
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.Note")
	props.Set(mapi.PrSentRepresentingSmtpAddress, representing)
	props.Set(mapi.PrSentRepresentingEmailAddress, representing)
	props.Set(mapi.PrSentRepresentingAddrType, "SMTP")
	// Sender identifies the real author; oxcmail emits a Sender header only when
	// it differs from the representing address (on-behalf), never for a plain send.
	props.Set(mapi.PrSenderSmtpAddress, sender)
	props.Set(mapi.PrSenderEmailAddress, sender)
	props.Set(mapi.PrSenderAddrType, "SMTP")
	props.Set(mapi.PrSubject, req.Subject)
	props.Set(mapi.PrClientSubmitTime, mapi.UnixToNTTime(time.Now()))
	props.Set(mapi.PrInternetMessageID, "<"+randomHex()+"@"+s.hostname+">")
	switch req.Importance {
	case "high":
		props.Set(mapi.PrImportance, int32(mapi.ImportanceHigh))
	case "low":
		props.Set(mapi.PrImportance, int32(mapi.ImportanceLow))
	}
	// Sensitivity mirrors importance: only a non-normal value sets the property,
	// which oxcmail.Export then emits as the RFC 2156 Sensitivity header.
	switch req.Sensitivity {
	case "personal":
		props.Set(mapi.PrSensitivity, int32(mapi.SensitivityPersonal))
	case "private":
		props.Set(mapi.PrSensitivity, int32(mapi.SensitivityPrivate))
	case "confidential":
		props.Set(mapi.PrSensitivity, int32(mapi.SensitivityConfidential))
	}
	if req.RequestReadReceipt {
		props.Set(mapi.PrReadReceiptRequested, true)
	}
	if req.RequestDeliveryReceipt {
		props.Set(mapi.PrOriginatorDeliveryReportRequested, true)
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
			// Never send the message without it. The sender attached this file on
			// purpose, so skipping it delivers a message the sender believes carries
			// an attachment it does not, and answers with success. Refuse instead and
			// name the file, so the failure is visible where it can be corrected.
			return nil, badAttachmentError{name: a.Filename}
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
		// No raw fallback: an unparseable string is not an address, and using it as
		// one is what lets a line break reach the header block. The handlers reject
		// such an entry up front, so nothing legitimate is dropped here.
		parsed, err := mail.ParseAddress(a)
		if err != nil {
			continue
		}
		name, addr := parsed.Name, parsed.Address
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

// maxScheduleAhead caps how far in the future a message may be scheduled, so a
// send-later cannot become a permanent dead letter occupying storage for years.
const maxScheduleAhead = 365 * 24 * time.Hour

// scheduleOutbox files a built message in the Outbox with a deferred send time
// (RFC3339), marked unsent, so the release worker delivers it when due. The time
// must be in the future and no more than maxScheduleAhead out, bounding both a
// past (immediately-due) and an absurd far-future schedule.
func scheduleOutbox(st *objectstore.Store, raw []byte, sendAt string) error {
	when, err := time.Parse(time.RFC3339, sendAt)
	if err != nil {
		return err
	}
	now := time.Now()
	if !when.After(now) {
		return fmt.Errorf("scheduled send time must be in the future")
	}
	if when.After(now.Add(maxScheduleAhead)) {
		return fmt.Errorf("scheduled send time is too far in the future (max 1 year)")
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
