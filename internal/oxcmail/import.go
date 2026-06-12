package oxcmail

import (
	stdmime "mime"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/mime"
)

// Import parses a raw RFC 5322 / MIME message into a MAPI Message: the
// header-derived envelope properties, the recipient table, and the body. It
// implements the MS-OXCMAIL internet-to-MAPI import path for the core property
// set; the long tail (TNEF, report/DSN/MDN, S/MIME, calendar, named-header
// passthrough) is not yet handled.
//
// opt.Resolver is reserved: the core property set carries no named properties,
// so no name-to-id resolution is performed yet.
func Import(raw []byte, opt Options) (*Message, error) {
	root := mime.ParseStructure(raw)
	msg := &Message{}

	// Default message class; the core header set never reclassifies plain mail.
	msg.Props.Set(mapi.PrMessageClass, "IPM.Note")

	hdr := root.Header()
	enumMailHead(hdr, msg)

	// Sender and sent-representing fill one from the other when a message names
	// only one identity.
	fillSenderRepresenting(msg)

	if !msg.Props.Has(mapi.PrImportance) {
		msg.Props.Set(mapi.PrImportance, int32(mapi.ImportanceNormal))
	}
	if !msg.Props.Has(mapi.PrSensitivity) {
		msg.Props.Set(mapi.PrSensitivity, int32(mapi.SensitivityNone))
	}

	// The original header block, captured verbatim.
	msg.Props.Set(mapi.PrTransportMessageHeaders, string(root.RawHeader()))

	// Submit time: the parsed Date, else the current time. Creation time mirrors
	// it, exactly as the import driver does.
	var stamp uint64
	if v, ok := msg.Props.Get(mapi.PrClientSubmitTime); ok {
		stamp = v.(uint64)
	} else {
		stamp = mapi.UnixToNTTime(time.Now())
		msg.Props.Set(mapi.PrClientSubmitTime, stamp)
	}
	msg.Props.Set(mapi.PrCreationTime, stamp)

	parseBody(root, msg)
	return msg, nil
}

// senderTags and representingTags name the two parallel identity property sets
// the From and Sender headers populate.
type addrTags struct {
	name, addrType, email, smtp, searchKey, entryID mapi.PropTag
}

var (
	senderTags = addrTags{
		name:      mapi.PrSenderName,
		addrType:  mapi.PrSenderAddrType,
		email:     mapi.PrSenderEmailAddress,
		smtp:      mapi.PrSenderSmtpAddress,
		searchKey: mapi.PrSenderSearchKey,
		entryID:   mapi.PrSenderEntryID,
	}
	representingTags = addrTags{
		name:      mapi.PrSentRepresentingName,
		addrType:  mapi.PrSentRepresentingAddrType,
		email:     mapi.PrSentRepresentingEmailAddress,
		smtp:      mapi.PrSentRepresentingSmtpAddress,
		searchKey: mapi.PrSentRepresentingSearchKey,
		entryID:   mapi.PrSentRepresentingEntryID,
	}
)

// enumMailHead dispatches the core header fields onto message properties per the
// MS-OXCMAIL header-to-property mapping. The From header populates the
// sent-representing identity (not the sender), and Sender populates the sender.
func enumMailHead(hdr textproto.MIMEHeader, msg *Message) {
	if v := hdr.Get("From"); v != "" {
		parseAddress(v, representingTags, &msg.Props)
	}
	if v := hdr.Get("Sender"); v != "" {
		parseAddress(v, senderTags, &msg.Props)
	}
	for _, v := range hdr.Values("To") {
		parseAddresses(v, mapi.RecipTo, msg)
	}
	for _, v := range hdr.Values("Cc") {
		parseAddresses(v, mapi.RecipCc, msg)
	}
	for _, v := range hdr.Values("Bcc") {
		parseAddresses(v, mapi.RecipBcc, msg)
	}
	if v := hdr.Get("Message-ID"); v != "" {
		msg.Props.Set(mapi.PrInternetMessageID, v)
	}
	if v := hdr.Get("Date"); v != "" {
		if t, err := mail.ParseDate(v); err == nil {
			msg.Props.Set(mapi.PrClientSubmitTime, mapi.UnixToNTTime(t))
		}
	}
	if v := hdr.Get("References"); v != "" {
		msg.Props.Set(mapi.PrInternetReferences, v)
	}
	if v := hdr.Get("In-Reply-To"); v != "" {
		msg.Props.Set(mapi.PrInReplyToID, v)
	}
	if v := hdr.Get("Sensitivity"); v != "" {
		msg.Props.Set(mapi.PrSensitivity, parseSensitivity(v))
	}
	// Priority: a later header overwrites an earlier one. They are applied
	// weakest-source-first so the MAPI-native Importance header wins a conflict.
	if v := hdr.Get("X-Priority"); v != "" {
		msg.Props.Set(mapi.PrImportance, parseXPriority(v))
	}
	if v := hdr.Get("Priority"); v != "" {
		msg.Props.Set(mapi.PrImportance, parsePriority(v))
	}
	if v := hdr.Get("X-MSMail-Priority"); v != "" {
		msg.Props.Set(mapi.PrImportance, parseImportance(v))
	}
	if v := hdr.Get("Importance"); v != "" {
		msg.Props.Set(mapi.PrImportance, parseImportance(v))
	}
	if v := hdr.Get("Subject"); v != "" {
		parseSubject(v, &msg.Props)
	}
	if v := hdr.Get("Thread-Topic"); v != "" {
		msg.Props.Set(mapi.PrConversationTopic, decodeHeaderWord(v))
	}
}

// parseAddress fills a single identity (sender or sent-representing) from one
// address header (From or Sender) per MS-OXCMAIL. The one-off ENTRYID and
// associated record key are not yet emitted: they require a one-off ENTRYID
// encoder, and the mail path reads only name and address, not the entryid.
func parseAddress(field string, t addrTags, props *mapi.PropertyValues) {
	addr, err := mail.ParseAddress(field)
	if err != nil {
		return
	}
	if addr.Name != "" {
		props.Set(t.name, addr.Name)
	} else if addr.Address != "" {
		props.Set(t.name, addr.Address)
	}
	if addr.Address == "" {
		return
	}
	props.Set(t.addrType, "SMTP")
	props.Set(t.email, addr.Address)
	props.Set(t.smtp, addr.Address)
	props.Set(t.searchKey, addressSearchKey(addr.Address))
}

// parseAddresses appends one recipient bag per address in a To/Cc/Bcc header,
// per MS-OXCMAIL for SMTP recipients. The one-off ENTRYID, recipient entryid,
// and record key are deferred (see parseAddress).
func parseAddresses(field string, rcptType int32, msg *Message) {
	list, err := mail.ParseAddressList(field)
	if err != nil {
		return
	}
	for _, addr := range list {
		if addr.Address == "" {
			continue
		}
		name := addr.Name
		if name == "" {
			name = addr.Address
		}
		r := mapi.PropertyValues{}
		r.Set(mapi.PrDisplayName, name)
		r.Set(mapi.PrTransmitableDisplayName, name)
		r.Set(mapi.PrAddrType, "SMTP")
		r.Set(mapi.PrEmailAddress, addr.Address)
		r.Set(mapi.PrSmtpAddress, addr.Address)
		r.Set(mapi.PrSearchKey, addressSearchKey(addr.Address))
		r.Set(mapi.PrObjectType, int32(mapi.ObjectTypeMailUser))
		r.Set(mapi.PrDisplayType, int32(mapi.DisplayTypeMailUser))
		r.Set(mapi.PrResponsibility, true)
		r.Set(mapi.PrRecipientFlags, int32(mapi.RecipientSendable))
		r.Set(mapi.PrRecipientType, rcptType)
		msg.Recipients = append(msg.Recipients, r)
	}
}

// addressSearchKey builds PR_SEARCH_KEY for an SMTP address: "SMTP:" followed by
// the uppercased address and a trailing NUL (the NUL is part of the value, as in
// the reference encoding).
func addressSearchKey(addr string) []byte {
	s := "SMTP:" + strings.ToUpper(addr)
	return append([]byte(s), 0)
}

// fillSenderRepresenting copies one identity set to the other when a message
// names only one, mirroring the import driver's fallback fill.
func fillSenderRepresenting(msg *Message) {
	hasSender := msg.Props.Has(mapi.PrSenderName) || msg.Props.Has(mapi.PrSenderSmtpAddress)
	hasRepr := msg.Props.Has(mapi.PrSentRepresentingName) || msg.Props.Has(mapi.PrSentRepresentingSmtpAddress)
	switch {
	case !hasSender:
		copyProp(msg, mapi.PrSenderName, mapi.PrSentRepresentingName)
		copyProp(msg, mapi.PrSenderSmtpAddress, mapi.PrSentRepresentingSmtpAddress)
		copyProp(msg, mapi.PrSenderAddrType, mapi.PrSentRepresentingAddrType)
		copyProp(msg, mapi.PrSenderEmailAddress, mapi.PrSentRepresentingEmailAddress)
		copyProp(msg, mapi.PrSenderSearchKey, mapi.PrSentRepresentingSearchKey)
		copyProp(msg, mapi.PrSenderEntryID, mapi.PrSentRepresentingEntryID)
	case !hasRepr:
		copyProp(msg, mapi.PrSentRepresentingName, mapi.PrSenderName)
		copyProp(msg, mapi.PrSentRepresentingSmtpAddress, mapi.PrSenderSmtpAddress)
		copyProp(msg, mapi.PrSentRepresentingAddrType, mapi.PrSenderAddrType)
		copyProp(msg, mapi.PrSentRepresentingEmailAddress, mapi.PrSenderEmailAddress)
		copyProp(msg, mapi.PrSentRepresentingSearchKey, mapi.PrSenderSearchKey)
		copyProp(msg, mapi.PrSentRepresentingEntryID, mapi.PrSenderEntryID)
	}
}

// copyProp sets dst from src when dst is absent and src is present (the import
// fallback fill of one identity set from the other).
func copyProp(msg *Message, dst, src mapi.PropTag) {
	if msg.Props.Has(dst) {
		return
	}
	if v, ok := msg.Props.Get(src); ok {
		msg.Props.Set(dst, v)
	}
}

// parseSubject sets PR_SUBJECT and splits a leading prefix ("RE: ", "FW: ")
// into PR_SUBJECT_PREFIX and PR_NORMALIZED_SUBJECT, per MS-OXCMAIL subject
// parsing plus the no-prefix defaults: both the prefix and the normalized
// subject are always set.
func parseSubject(field string, props *mapi.PropertyValues) {
	subject := decodeHeaderWord(field)
	props.Set(mapi.PrSubject, subject)
	if prefix, normalized, ok := splitSubjectPrefix(subject); ok {
		props.Set(mapi.PrSubjectPrefix, prefix)
		props.Set(mapi.PrNormalizedSubject, normalized)
	} else {
		props.Set(mapi.PrSubjectPrefix, "")
		props.Set(mapi.PrNormalizedSubject, subject)
	}
}

// splitSubjectPrefix detects a "<label>: " prefix where label is 1-3 characters,
// contains no ':' or ' ', and does not start with a digit (the MS-OXCMAIL
// subject prefix rules). The returned prefix includes the ": " separator.
func splitSubjectPrefix(subject string) (prefix, normalized string, ok bool) {
	idx := strings.Index(subject, ": ")
	if idx <= 0 {
		return "", "", false
	}
	label := subject[:idx]
	if n := len([]rune(label)); n < 1 || n > 3 {
		return "", "", false
	}
	for i, r := range label {
		if r == ':' || r == ' ' {
			return "", "", false
		}
		if i == 0 && r >= '0' && r <= '9' {
			return "", "", false
		}
	}
	return subject[:idx+2], subject[idx+2:], true
}

// parseSensitivity maps a Sensitivity header to PR_SENSITIVITY per MS-OXCMAIL.
func parseSensitivity(s string) int32 {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "personal":
		return mapi.SensitivityPersonal
	case "private":
		return mapi.SensitivityPrivate
	case "company-confidential":
		return mapi.SensitivityConfidential
	}
	return mapi.SensitivityNone
}

// parseImportance maps an Importance / X-MSMail-Priority header to PR_IMPORTANCE
// per MS-OXCMAIL.
func parseImportance(s string) int32 {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return mapi.ImportanceLow
	case "high":
		return mapi.ImportanceHigh
	}
	return mapi.ImportanceNormal
}

// parsePriority maps a Priority header to PR_IMPORTANCE per MS-OXCMAIL.
func parsePriority(s string) int32 {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "non-urgent":
		return mapi.ImportanceLow
	case "urgent":
		return mapi.ImportanceHigh
	}
	return mapi.ImportanceNormal
}

// parseXPriority maps an X-Priority header to PR_IMPORTANCE per MS-OXCMAIL:
// 4-5 are low, 1-2 high, everything else normal.
func parseXPriority(s string) int32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return mapi.ImportanceNormal
	}
	switch s[0] {
	case '4', '5':
		return mapi.ImportanceLow
	case '1', '2':
		return mapi.ImportanceHigh
	}
	return mapi.ImportanceNormal
}

// maxBodyDepth bounds how deep the body-part search recurses into nested
// multiparts.
const maxBodyDepth = 10

// bodyParts holds the parts selected for the body representations. Multiple HTML
// parts (joined related bodies) and the calendar/enriched tail are recorded but
// only the single-HTML and plain cases are consumed by the core path.
type bodyParts struct {
	plain    *mime.Part
	htmls    []*mime.Part
	enriched *mime.Part
}

// parseBody selects the message's body parts and fills the body properties:
// PR_BODY from the plain part (charset-decoded to UTF-8) and PR_HTML +
// PR_INTERNET_CPID from a single HTML part (stored as raw bytes in its original
// charset). Multiple-HTML joining, enriched, and calendar bodies are deferred.
func parseBody(root *mime.Part, msg *Message) {
	var bp bodyParts
	selectParts(root, &bp, 0)
	if bp.plain != nil {
		if text, err := bp.plain.DecodedText(); err == nil {
			msg.Props.Set(mapi.PrBody, text)
		}
	}
	if len(bp.htmls) == 1 {
		setHTMLBody(msg, bp.htmls[0])
	}
}

// selectParts walks the MIME tree and selects the parts used for the body,
// porting the MS-OXCMAIL body-part selection. level 0 is the root; a part with a
// Content-Disposition of attachment is never a body part. A multipart/alternative
// takes the best of each body type among its children; other multiparts take the
// first plain part and (when the first child is HTML) join the HTML parts.
func selectParts(part *mime.Part, info *bodyParts, level int) {
	if strings.HasPrefix(part.Disposition, "attachment") {
		return
	}
	if len(part.Children) == 0 {
		switch {
		case part.Type == "text" && part.Subtype == "plain":
			info.plain = part
		case part.Type == "text" && part.Subtype == "html":
			info.htmls = append(info.htmls, part)
		case part.Type == "text" && part.Subtype == "enriched":
			info.enriched = part
		}
		return
	}
	if level >= maxBodyDepth {
		return
	}
	level++
	alt := part.Type == "multipart" && part.Subtype == "alternative"
	hjoinEnabled := false
	for idx, child := range part.Children {
		var cld bodyParts
		selectParts(child, &cld, level)
		if alt {
			if cld.plain != nil {
				info.plain = cld.plain
			}
			if len(cld.htmls) > 0 {
				info.htmls = cld.htmls
			}
			if cld.enriched != nil {
				info.enriched = cld.enriched
			}
			continue
		}
		if idx == 0 && len(cld.htmls) > 0 {
			hjoinEnabled = true
		}
		if cld.plain != nil && info.plain == nil {
			info.plain = cld.plain
		}
		if hjoinEnabled {
			info.htmls = append(info.htmls, cld.htmls...)
		}
		if cld.enriched != nil && info.enriched == nil {
			info.enriched = cld.enriched
		}
	}
}

// setHTMLBody stores an HTML part as PR_HTML (transfer-decoded raw bytes in the
// part's own charset) and PR_INTERNET_CPID (that charset's code page). Unlike
// the plain body, the HTML is not converted to UTF-8.
func setHTMLBody(msg *Message, part *mime.Part) {
	raw, err := part.DecodedContent()
	if err != nil {
		return
	}
	charset := part.Params["charset"]
	if charset == "" {
		charset = "us-ascii"
	}
	msg.Props.Set(mapi.PrInternetCodepage, csetToCPID(charset))
	msg.Props.Set(mapi.PrHTML, raw)
}

// decodeHeaderWord decodes RFC 2047 encoded-words in a header value, leaving
// plain text unchanged.
func decodeHeaderWord(s string) string {
	if d, err := (&stdmime.WordDecoder{}).DecodeHeader(s); err == nil {
		return d
	}
	return s
}
