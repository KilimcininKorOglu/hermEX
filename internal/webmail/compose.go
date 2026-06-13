package webmail

import (
	"bytes"
	"encoding/json"
	"html"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// sentName and draftsName are the display paths of the Sent and Drafts folders,
// used to navigate there after a message is filed. The copies themselves are
// filed by the folders' fixed ids.
const (
	sentName   = "Sent Items"
	draftsName = "Drafts"
	outboxName = "Outbox"
)

// composeView is the data the compose template renders, covering both a blank
// compose and a reply/forward prefill.
type composeView struct {
	Title        string
	From         string   // selected sender (defaults to the session user)
	FromOptions  []string // identities the user may send as (own address + aliases)
	To           string
	Cc           string
	Bcc          string
	Subject      string
	Body         string // plain-text body (also the text/plain alternative in HTML mode)
	BodyHTML     string // HTML body, set by the editor when Format == "html"
	Format       string // "", "plain", "html"
	Importance   string // "", "high", "low"
	Sensitivity  string // "", "personal", "private", "confidential"
	ReadReceipt  bool
	InReplyTo    string // carried as a hidden field, written as In-Reply-To on send
	References   string // carried as a hidden field, written as References on send
	AttachFolder string // forward-as-attachment: source folder to embed at send
	AttachUID    string // forward-as-attachment: source uid to embed at send
	DraftFolder  string // draft being edited: source folder (carried so a re-save replaces it)
	DraftUID     string // draft being edited: source uid
	Error        string
	Notice       string
}

// identities returns the addresses user may send as. It fails closed: if the
// directory cannot enumerate identities, the user may still send as themselves
// but as no one else.
func (s *Server) identities(user string) []string {
	id, ok := s.auth.(directory.Identifier)
	if !ok {
		return []string{user}
	}
	addrs, err := id.Identities(user)
	if err != nil || len(addrs) == 0 {
		return []string{user}
	}
	return addrs
}

// gateFrom returns submitted if it is one of the user's permitted identities
// (case-insensitive), else the session user. The submitted value is never
// emitted unless authorized, so an editable From cannot spoof another sender.
func gateFrom(submitted, sessUser string, allowed []string) string {
	want := strings.ToLower(strings.TrimSpace(submitted))
	if want == "" {
		return sessUser
	}
	for _, a := range allowed {
		if strings.ToLower(a) == want {
			return submitted
		}
	}
	return sessUser
}

func (s *Server) handleComposeForm(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	idents := s.identities(sess.user)
	st, err := objectstore.Open(sess.mailboxPath)
	if err != nil {
		http.Error(w, "mailbox unavailable", http.StatusInternalServerError)
		return
	}
	defer st.Close()
	settings, err := loadSettings(st)
	if err != nil {
		settings = defaultSettings()
	}

	action := r.URL.Query().Get("action")
	if action == "" {
		v := composeView{Title: "New message", From: sess.user, FromOptions: idents, Format: settings.ComposeFormat}
		applySignature(&v, settings, action)
		s.render(w, "compose", v)
		return
	}
	// Reply/forward variants prefill from a source message.
	folder := r.URL.Query().Get("folder")
	uid64, err := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
	if err != nil {
		s.render(w, "compose", composeView{Title: "New message", From: sess.user, FromOptions: idents, Format: settings.ComposeFormat})
		return
	}
	folders, err := st.ListFolders()
	if err != nil {
		http.Error(w, "cannot read folders", http.StatusInternalServerError)
		return
	}
	folderID, found := resolveFolder(folders, folder)
	if !found {
		http.NotFound(w, r)
		return
	}
	raw, err := st.GetMessageRaw(folderID, uint32(uid64))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v := buildComposeFromSource(action, folder, uint32(uid64), raw, sess.user)
	v.From = sess.user
	v.FromOptions = idents
	// A reopened draft carries its own format (editdraft sets it from the draft's
	// shape); for new/reply/forward Format is empty, so fall back to the user's
	// default. The draft's own format must win, or an HTML draft reopened by a
	// plain-default user would be re-saved as text/plain and lose its markup.
	if v.Format == "" {
		v.Format = settings.ComposeFormat
	}
	applySignature(&v, settings, action)
	s.render(w, "compose", v)
}

// applySignature top-posts the configured signature into the prefill: the new
// message signature for a blank compose, the reply/forward signature for those
// actions, and none for edit-as-new (its body is already complete). The plain
// rendering goes into Body (the text/plain alternative and no-JS source) and the
// HTML rendering into BodyHTML, both above the existing quote. The quote itself
// stays plain text — never the original message's raw markup.
func applySignature(v *composeView, cfg webmailSettings, action string) {
	var id string
	switch action {
	case "":
		id = cfg.DefaultSignatureNew
	case "reply", "replyall", "forward", "forwardasattach":
		id = cfg.DefaultSignatureReply
	default: // editasnew or unknown: leave the body untouched
		return
	}
	sig, ok := cfg.signatureByID(id)
	if !ok {
		return
	}
	sigPlain, sigHTML := signatureBodies(sig)
	quote := v.Body
	v.Body = topPost(sigPlain, quote)
	v.BodyHTML = topPostHTML(sigHTML, quoteToHTML(quote))
}

// signatureBodies returns a signature's plain-text and HTML renderings. A plain
// signature is escaped for its HTML form; an HTML signature is stripped to text
// for its plain form.
func signatureBodies(sig signature) (plain, htmlBody string) {
	if sig.IsHTML {
		return stripHTML(sig.HTML), sig.HTML
	}
	return sig.HTML, quoteToHTML(sig.HTML)
}

// topPost places sig above body, separated by a blank line; either part may be
// empty.
func topPost(sig, body string) string {
	switch {
	case sig == "":
		return body
	case body == "":
		return sig
	default:
		return sig + "\n\n" + body
	}
}

// topPostHTML places sigHTML above bodyHTML, separated by a blank line.
func topPostHTML(sigHTML, bodyHTML string) string {
	switch {
	case sigHTML == "":
		return bodyHTML
	case bodyHTML == "":
		return sigHTML
	default:
		return sigHTML + "<br><br>" + bodyHTML
	}
}

// quoteToHTML renders a plain-text quote as escaped HTML, preserving line breaks,
// so it can be seeded into the editor without exposing it to markup injection.
func quoteToHTML(plain string) string {
	if plain == "" {
		return ""
	}
	return strings.ReplaceAll(html.EscapeString(plain), "\n", "<br>\n")
}

func (s *Server) handleComposeSubmit(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	idents := s.identities(sess.user)
	v := composeView{
		Title:        "New message",
		From:         gateFrom(r.FormValue("from"), sess.user, idents),
		FromOptions:  idents,
		To:           strings.TrimSpace(r.FormValue("to")),
		Cc:           strings.TrimSpace(r.FormValue("cc")),
		Bcc:          strings.TrimSpace(r.FormValue("bcc")),
		Subject:      r.FormValue("subject"),
		Body:         r.FormValue("body"),
		BodyHTML:     r.FormValue("bodyhtml"),
		Format:       r.FormValue("format"),
		Importance:   r.FormValue("importance"),
		Sensitivity:  r.FormValue("sensitivity"),
		ReadReceipt:  r.FormValue("readreceipt") != "",
		InReplyTo:    strings.TrimSpace(r.FormValue("inreplyto")),
		References:   strings.TrimSpace(r.FormValue("references")),
		AttachFolder: r.FormValue("attachfolder"),
		AttachUID:    r.FormValue("attachuid"),
		DraftFolder:  r.FormValue("draftfolder"),
		DraftUID:     r.FormValue("draftuid"),
	}

	// Saving a draft files the compose in Drafts without sending; no recipients
	// are required. Autosave posts the same action with Accept: application/json
	// and gets a small JSON reply instead of a re-rendered page.
	if r.FormValue("action") == "savedraft" {
		asJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
		s.saveDraft(w, sess.mailboxPath, &v, asJSON)
		return
	}

	// Scheduling a send files the compose in the Outbox with a deferred-send time
	// instead of delivering now; the worker releases it when due.
	if r.FormValue("action") == "sendlater" {
		s.scheduleSend(w, r, sess.mailboxPath, &v)
		return
	}

	recipients := append(splitAddresses(v.To), splitAddresses(v.Cc)...)
	recipients = append(recipients, splitAddresses(v.Bcc)...)
	if len(recipients) == 0 {
		v.Error = "At least one recipient is required."
		s.render(w, "compose", v)
		return
	}

	o := v.outgoing(s.hostname)
	// Forward-as-attachment embeds the source message verbatim as message/rfc822.
	if v.AttachFolder != "" && v.AttachUID != "" {
		if embed, err := s.loadRaw(sess.mailboxPath, v.AttachFolder, v.AttachUID); err == nil {
			o.Embed = embed
		}
	}
	// Build the wire message once (no Bcc header — Bcc must not reach To/Cc/Bcc
	// recipients). The sender's Sent copy carries a Bcc header for the record.
	deliveryRaw, err := buildMessage(o)
	if err != nil {
		v.Error = "Could not build the message: " + err.Error()
		s.render(w, "compose", v)
		return
	}
	sentRaw := insertBcc(deliveryRaw, v.Bcc)

	unresolved, err := mta.Deliver(s.accounts, recipients, deliveryRaw, time.Now())
	if err != nil {
		v.Error = "Delivery failed: " + err.Error()
		s.render(w, "compose", v)
		return
	}
	if err := saveToSent(sess.mailboxPath, sentRaw); err != nil {
		v.Error = "Saved no Sent copy: " + err.Error()
		s.render(w, "compose", v)
		return
	}

	// A draft that has now been sent is removed from Drafts (delete only after a
	// successful send, so a failed send leaves the draft intact).
	if v.DraftUID != "" {
		deleteDraft(sess.mailboxPath, v.DraftUID)
	}

	// Local recipients are delivered and a Sent copy is stored. If some
	// addresses have no local mailbox, report them (there is no relay yet)
	// rather than pretend they were delivered.
	if len(unresolved) > 0 {
		s.render(w, "compose", composeView{
			Title:       "New message",
			From:        sess.user,
			FromOptions: idents,
			Notice:      "Delivered locally and saved to Sent. No local mailbox (and no external relay yet) for: " + strings.Join(unresolved, ", "),
		})
		return
	}
	http.Redirect(w, r, "/mail?folder="+url.QueryEscape(sentName), http.StatusSeeOther)
}

// saveDraft files the compose as a draft in the Drafts folder — replacing the
// draft being edited when DraftUID is set (there is no in-place updater, so a
// re-save deletes the old copy and appends a fresh one with a new uid) — so a
// subsequent save replaces the same draft. The draft keeps Bcc and every field
// so it re-opens complete. With asJSON (autosave) it replies with the new draft
// uid as JSON; otherwise it re-renders the compose page with a confirmation.
func (s *Server) saveDraft(w http.ResponseWriter, mailboxPath string, v *composeView, asJSON bool) {
	built, err := buildMessage(v.outgoing(s.hostname))
	if err != nil {
		s.draftError(w, v, asJSON, "Could not build the draft: "+err.Error())
		return
	}
	draftRaw := insertBcc(built, v.Bcc)

	st, err := objectstore.Open(mailboxPath)
	if err != nil {
		s.draftError(w, v, asJSON, "mailbox unavailable")
		return
	}
	defer st.Close()

	draftFID := int64(mapi.PrivateFIDDraft)
	if v.DraftUID != "" {
		if uid, err := strconv.ParseUint(v.DraftUID, 10, 32); err == nil {
			st.DeleteMessage(draftFID, uint32(uid))
		}
	}
	info, err := st.AppendMessage(draftFID, draftRaw, time.Now(), objectstore.FlagSeen|objectstore.FlagDraft)
	if err != nil {
		s.draftError(w, v, asJSON, "Could not save draft: "+err.Error())
		return
	}
	v.DraftFolder = draftsName
	v.DraftUID = strconv.FormatUint(uint64(info.UID), 10)
	if asJSON {
		writeJSON(w, http.StatusOK, map[string]string{
			"draftUid": v.DraftUID,
			"savedAt":  time.Now().Format(time.RFC3339),
		})
		return
	}
	v.Notice = "Draft saved."
	s.render(w, "compose", *v)
}

// draftError reports a draft-save failure as JSON for autosave, or as an
// in-page error for the manual save.
func (s *Server) draftError(w http.ResponseWriter, v *composeView, asJSON bool, msg string) {
	if asJSON {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": msg})
		return
	}
	v.Error = msg
	s.render(w, "compose", *v)
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// scheduleSend files the compose in the Outbox for delayed delivery: it requires
// at least one recipient and a future time, stores the with-Bcc message (so every
// recipient is recoverable when the worker releases it), and stamps it with the
// deferred-send time and the unsent flag. The send-later worker delivers it when
// due. Scheduling an opened draft removes that draft, which now lives as the
// Outbox copy.
func (s *Server) scheduleSend(w http.ResponseWriter, r *http.Request, mailboxPath string, v *composeView) {
	recipients := append(splitAddresses(v.To), splitAddresses(v.Cc)...)
	recipients = append(recipients, splitAddresses(v.Bcc)...)
	if len(recipients) == 0 {
		v.Error = "At least one recipient is required."
		s.render(w, "compose", *v)
		return
	}
	// A datetime-local field has no timezone, so it is the user's wall-clock time;
	// interpret it in the server's local zone and store the absolute instant.
	when, err := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(r.FormValue("sendat")), time.Local)
	if err != nil {
		v.Error = "Choose a valid date and time to send."
		s.render(w, "compose", *v)
		return
	}
	if !when.After(time.Now()) {
		v.Error = "The scheduled time must be in the future."
		s.render(w, "compose", *v)
		return
	}

	built, err := buildMessage(v.outgoing(s.hostname))
	if err != nil {
		v.Error = "Could not build the message: " + err.Error()
		s.render(w, "compose", *v)
		return
	}
	raw := insertBcc(built, v.Bcc)
	st, err := objectstore.Open(mailboxPath)
	if err != nil {
		v.Error = "mailbox unavailable"
		s.render(w, "compose", *v)
		return
	}
	defer st.Close()

	info, err := st.AppendMessage(int64(mapi.PrivateFIDOutbox), raw, time.Now(), objectstore.FlagSeen)
	if err != nil {
		v.Error = "Could not schedule the message: " + err.Error()
		s.render(w, "compose", *v)
		return
	}
	// The deferred-send time tells the worker when to release the message; the
	// unsent flag marks it pending submission.
	if err := st.SetMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrDeferredSendTime, Value: mapi.UnixToNTTime(when)},
		{Tag: mapi.PrMessageFlags, Value: int32(mapi.MsgFlagUnsent)},
	}); err != nil {
		v.Error = "Could not schedule the message: " + err.Error()
		s.render(w, "compose", *v)
		return
	}
	if v.DraftUID != "" {
		if uid, perr := strconv.ParseUint(v.DraftUID, 10, 32); perr == nil {
			st.DeleteMessage(int64(mapi.PrivateFIDDraft), uint32(uid))
		}
	}
	http.Redirect(w, r, "/mail?folder="+url.QueryEscape(outboxName), http.StatusSeeOther)
}

// deleteDraft removes a draft from the Drafts folder by its uid (best-effort,
// used once a draft has been sent).
func deleteDraft(mailboxPath, uidStr string) {
	uid, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		return
	}
	st, err := objectstore.Open(mailboxPath)
	if err != nil {
		return
	}
	defer st.Close()
	st.DeleteMessage(int64(mapi.PrivateFIDDraft), uint32(uid))
}

// insertBcc returns raw with a Bcc header spliced into the top-level header
// block (just before the header/body separator). The delivered wire copy never
// carries Bcc; only the sender's Sent copy does, so recipients cannot see the
// blind-copy list.
func insertBcc(raw []byte, bcc string) []byte {
	bcc = strings.TrimSpace(bcc)
	if bcc == "" {
		return raw
	}
	i := bytes.Index(raw, []byte("\r\n\r\n"))
	if i < 0 {
		return raw
	}
	var b bytes.Buffer
	b.Write(raw[:i])
	b.WriteString("\r\nBcc: ")
	b.WriteString(bcc)
	b.Write(raw[i:])
	return b.Bytes()
}

// loadRaw opens the mailbox and returns one message's raw bytes by folder path
// and uid, used to embed a forwarded message at send time.
func (s *Server) loadRaw(mailboxPath, folder, uidStr string) ([]byte, error) {
	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		return nil, err
	}
	st, err := objectstore.Open(mailboxPath)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	folders, err := st.ListFolders()
	if err != nil {
		return nil, err
	}
	folderID, found := resolveFolder(folders, folder)
	if !found {
		return nil, objectstore.ErrNotFound
	}
	return st.GetMessageRaw(folderID, uint32(uid64))
}

// composeAttachment is one attachment to add to a composed message: an uploaded
// file, or (when ContentID/Inline is set) an inline image referenced by the HTML
// body via cid:. It maps to a by-value MAPI attachment bag that oxcmail.Export
// renders into the MIME tree.
type composeAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
	ContentID   string // bare token (no <>); set => inline, referenced by the HTML body
	Inline      bool   // Content-Disposition: inline (a multipart/related member)
}

// toAttachment builds the oxcmail attachment bag for this attachment. An inline
// attachment carries the MHTML-reference flag + Content-ID so Export emits it
// inside multipart/related as a cid: target.
func (a composeAttachment) toAttachment() oxcmail.Attachment {
	var p mapi.PropertyValues
	p.Set(mapi.PrAttachMethod, int32(mapi.AttachByValue))
	p.Set(mapi.PrAttachDataBin, a.Data)
	if a.ContentType != "" {
		p.Set(mapi.PrAttachMimeTag, a.ContentType)
	}
	if a.Filename != "" {
		p.Set(mapi.PrAttachLongFilename, a.Filename)
	}
	if a.ContentID != "" {
		p.Set(mapi.PrAttachContentID, a.ContentID)
	}
	if a.Inline || a.ContentID != "" {
		p.Set(mapi.PrAttachFlags, int32(mapi.AttMhtmlRef))
	}
	return oxcmail.Attachment{Props: p}
}

// outgoing is the set of fields buildMessage turns into an RFC 5322 message (by
// building a MAPI object and exporting it through oxcmail).
type outgoing struct {
	From        string
	To          string
	Cc          string
	Subject     string
	Body        string // plain-text body, and the text/plain alternative in HTML mode
	BodyHTML    string // HTML body; with Format=="html" the message carries a text/html alternative
	Format      string // "html" => multipart/alternative (text/plain + text/html)
	Importance  string // "high"/"low" → Importance header
	Sensitivity string // "personal"/"private"/"confidential" → Sensitivity header
	ReadReceipt bool   // → Disposition-Notification-To (RFC 8098)
	InReplyTo   string
	References  string
	Embed       []byte              // forward-as-attachment: embedded as a message/rfc822 attachment
	Attachments []composeAttachment // uploaded files and inline images
	Hostname    string
}

// outgoing assembles the message-building fields shared by sending, saving a
// draft, and scheduling a send. The caller adds Embed (forward-as-attachment)
// where it applies.
func (v composeView) outgoing(hostname string) outgoing {
	return outgoing{
		From: v.From, To: v.To, Cc: v.Cc, Subject: v.Subject,
		Body: v.Body, BodyHTML: v.BodyHTML, Format: v.Format,
		Importance: v.Importance, Sensitivity: v.Sensitivity,
		ReadReceipt: v.ReadReceipt, InReplyTo: v.InReplyTo, References: v.References,
		Hostname: hostname,
	}
}

// buildMessage assembles an RFC 5322 message from the compose fields by building
// a MAPI message object and exporting it through oxcmail — the same converter
// that re-synthesizes every stored message — so multipart/alternative, inline
// images (multipart/related), and attachments (multipart/mixed) are produced by
// one proven path. Bcc is never put on the object (Export would emit a Bcc
// header to recipients); the caller splices it onto the stored copy via insertBcc.
func buildMessage(o outgoing) ([]byte, error) {
	return oxcmail.Export(composeToMessage(o), oxcmail.Options{})
}

// composeToMessage maps the compose fields onto a MAPI message object: the
// sent-representing identity, To/Cc recipient bags, the headers Export
// re-emits (Subject/Date/Message-ID/Importance/Sensitivity/read-receipt/
// In-Reply-To/References), the plain and optional HTML body, the
// forward-as-attachment embed (a message/rfc822 attachment), and any uploaded or
// inline attachments. Message-ID and Date (PrClientSubmitTime) MUST be set here:
// Export has no fallback, and the delivered wire copy is not re-imported before
// it is sent.
func composeToMessage(o outgoing) *oxcmail.Message {
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, "IPM.Note")
	if o.From != "" {
		props.Set(mapi.PrSentRepresentingSmtpAddress, o.From)
		props.Set(mapi.PrSentRepresentingEmailAddress, o.From)
		props.Set(mapi.PrSentRepresentingAddrType, "SMTP")
	}
	props.Set(mapi.PrSubject, o.Subject)
	props.Set(mapi.PrClientSubmitTime, mapi.UnixToNTTime(time.Now()))
	props.Set(mapi.PrInternetMessageID, "<"+randomToken()[:24]+"@"+o.Hostname+">")
	if o.InReplyTo != "" {
		props.Set(mapi.PrInReplyToID, o.InReplyTo)
	}
	if o.References != "" {
		props.Set(mapi.PrInternetReferences, o.References)
	}
	switch o.Importance {
	case "high":
		props.Set(mapi.PrImportance, int32(mapi.ImportanceHigh))
	case "low":
		props.Set(mapi.PrImportance, int32(mapi.ImportanceLow))
	}
	switch o.Sensitivity {
	case "personal":
		props.Set(mapi.PrSensitivity, int32(mapi.SensitivityPersonal))
	case "private":
		props.Set(mapi.PrSensitivity, int32(mapi.SensitivityPrivate))
	case "confidential":
		props.Set(mapi.PrSensitivity, int32(mapi.SensitivityConfidential))
	}
	if o.ReadReceipt {
		props.Set(mapi.PrReadReceiptRequested, true)
	}
	props.Set(mapi.PrBody, toCRLF(o.Body))
	if o.Format == "html" && strings.TrimSpace(o.BodyHTML) != "" {
		props.Set(mapi.PrHTML, []byte(toCRLF(o.BodyHTML)))
	}

	msg := &oxcmail.Message{Props: props}
	msg.Recipients = append(recipientBags(o.To, mapi.RecipTo), recipientBags(o.Cc, mapi.RecipCc)...)

	if len(o.Embed) > 0 {
		var att mapi.PropertyValues
		att.Set(mapi.PrAttachMimeTag, "message/rfc822")
		att.Set(mapi.PrAttachLongFilename, "forwarded.eml")
		att.Set(mapi.PrAttachMethod, int32(mapi.AttachByValue))
		att.Set(mapi.PrAttachDataBin, o.Embed)
		msg.Attachments = append(msg.Attachments, oxcmail.Attachment{Props: att})
	}
	for _, a := range o.Attachments {
		msg.Attachments = append(msg.Attachments, a.toAttachment())
	}
	return msg
}

// recipientBags parses a comma-separated address field into one recipient
// property bag per address, of the given recipient type (To/Cc). A malformed
// field falls back to treating each comma-separated token as a bare address.
func recipientBags(field string, rcptType int32) []mapi.PropertyValues {
	addrs, err := mail.ParseAddressList(field)
	if err != nil {
		for _, a := range splitAddresses(field) {
			addrs = append(addrs, &mail.Address{Address: a})
		}
	}
	var bags []mapi.PropertyValues
	for _, a := range addrs {
		var bag mapi.PropertyValues
		bag.Set(mapi.PrRecipientType, rcptType)
		bag.Set(mapi.PrAddrType, "SMTP")
		bag.Set(mapi.PrEmailAddress, a.Address)
		bag.Set(mapi.PrSmtpAddress, a.Address)
		if a.Name != "" {
			bag.Set(mapi.PrDisplayName, a.Name)
		}
		bags = append(bags, bag)
	}
	return bags
}

// toCRLF normalizes a string's line endings to CRLF for the wire/store.
func toCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// saveToSent appends a copy of a sent message to the sender's Sent folder,
// creating it on first use and marking the copy \Seen.
func saveToSent(mailboxPath string, raw []byte) error {
	st, err := objectstore.Open(mailboxPath)
	if err != nil {
		return err
	}
	defer st.Close()
	_, err = st.AppendMessage(int64(mapi.PrivateFIDSentItems), raw, time.Now(), objectstore.FlagSeen)
	return err
}

// splitAddresses splits a comma-separated recipient field into trimmed,
// non-empty addresses.
func splitAddresses(s string) []string {
	var out []string
	for addr := range strings.SplitSeq(s, ",") {
		if a := strings.TrimSpace(addr); a != "" {
			out = append(out, a)
		}
	}
	return out
}
