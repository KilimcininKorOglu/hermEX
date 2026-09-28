package oxcmail

import (
	"bytes"
	stdmime "mime"
	"net/textproto"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/tnef"
)

// A message Outlook or Exchange sends with properties MIME cannot carry has an
// application/ms-tnef part (winmail.dat). [MS-OXCMAIL] 2.2.3.8 reads that part
// into the message: its properties, its attachments and, for a legacy delivery
// report, its recipient table. The MIME wrapper stays authoritative: a property
// the headers already set is not replaced.

// maxTNEFNames bounds how many named properties one TNEF part may carry into the
// store. The store bounds the names it allocates separately.
const maxTNEFNames = 256

// tnefContext carries what applying one decoded stream needs.
type tnefContext struct {
	ids   map[mapi.PropertyName]uint16
	stamp uint64
	depth int
}

// claimTNEF decodes the message's TNEF part, when it has one that decodes, and
// marks the part as consumed so it is no attachment of its own. A part that does
// not decode stays an attachment.
func claimTNEF(root *mime.Part, consumed map[*mime.Part]bool) *tnef.Message {
	part := findTNEF(root)
	if part == nil {
		return nil
	}
	decoded := decodeTNEFPart(part, root.Header())
	if decoded != nil {
		consumed[part] = true
	}
	return decoded
}

// findTNEF returns the first application/ms-tnef leaf of a message, or nil.
func findTNEF(part *mime.Part) *mime.Part {
	if len(part.Children) == 0 {
		if isTNEFPart(part) {
			return part
		}
		return nil
	}
	for _, c := range part.Children {
		if p := findTNEF(c); p != nil {
			return p
		}
	}
	return nil
}

// isTNEFPart reports whether a part is a TNEF body part.
func isTNEFPart(p *mime.Part) bool {
	return strings.EqualFold(p.Type, "application") && strings.EqualFold(p.Subtype, "ms-tnef")
}

// decodeTNEFPart decodes a TNEF part, or returns nil when the stream does not
// conform or belongs to another message: a stream whose correlation key differs
// from the X-MS-TNEF-Correlator header was carried over from a message a
// TNEF-unaware client forwarded, and is then kept as a plain attachment.
func decodeTNEFPart(part *mime.Part, hdr textproto.MIMEHeader) *tnef.Message {
	data, err := part.DecodedContent()
	if err != nil {
		return nil
	}
	t, err := tnef.Decode(data, decodeANSI)
	if err != nil {
		return nil
	}
	correlator := strings.TrimSpace(hdr.Get("X-MS-TNEF-Correlator"))
	if key, ok := tnefCorrelationKey(t); correlator != "" && ok && key != correlator {
		return nil
	}
	return t
}

// tnefCorrelationKey returns the stream's PidTagTnefCorrelationKey as text.
func tnefCorrelationKey(t *tnef.Message) (string, bool) {
	for _, p := range t.Props {
		if p.Tag == mapi.PrTnefCorrelationKey {
			if b, ok := p.Value.([]byte); ok {
				return string(bytes.TrimRight(b, "\x00")), true
			}
		}
	}
	return "", false
}

// decodeANSI converts an 8-bit TNEF string from its declared code page.
func decodeANSI(raw []byte, codepage uint32) string {
	if codepage == 0 {
		return mime.DecodeCharset(raw, "windows-1252")
	}
	return mime.DecodeCharset(raw, cpidToCset(int32(codepage))) // #nosec G115 -- a Windows code page id fits in 32 bits either way
}

// applyTNEF sets the decoded properties, attachments and, for a delivery report,
// recipients on the message.
func applyTNEF(msg *Message, t *tnef.Message, opt Options, stamp uint64) error {
	ids, err := resolveTNEFNames(t, opt.ForeignResolver)
	if err != nil {
		return err
	}
	c := tnefContext{ids: ids, stamp: stamp}
	c.setProps(&msg.Props, t.Props, false)
	if isDeliveryReportClass(propString(msg.Props, mapi.PrMessageClass)) {
		c.addRecipients(msg, t)
	}
	for _, a := range t.Attachments {
		msg.Attachments = append(msg.Attachments, c.attachment(a))
	}
	return nil
}

// isDeliveryReportClass reports whether a class is a delivery report, the one kind
// whose TNEF recipient table is read ([MS-OXTNEF] 2.1.3.5.2.2).
func isDeliveryReportClass(class string) bool {
	u := strings.ToUpper(class)
	return strings.HasPrefix(u, "REPORT.") && (strings.HasSuffix(u, ".DR") || strings.HasSuffix(u, ".NDR"))
}

// resolveTNEFNames maps the named properties of a stream, its attachments and its
// recipients to store ids through the quota-bound resolver.
func resolveTNEFNames(t *tnef.Message, resolve ForeignResolver) (map[mapi.PropertyName]uint16, error) {
	ids := map[mapi.PropertyName]uint16{}
	names := tnefNames(t)
	if resolve == nil || len(names) == 0 {
		return ids, nil
	}
	got, err := resolve(names)
	if err != nil {
		return nil, err
	}
	for i, n := range names {
		ids[n] = got[i]
	}
	return ids, nil
}

// tnefNames lists the distinct named properties of a stream, its recipients and
// its attachments, at most maxTNEFNames of them.
func tnefNames(t *tnef.Message) []mapi.PropertyName {
	seen := map[mapi.PropertyName]bool{}
	var names []mapi.PropertyName
	lists := append(append([][]tnef.Prop{t.Props}, t.Recipients...), attachmentProps(t)...)
	for _, props := range lists {
		for _, p := range props {
			if p.Name != nil && !seen[*p.Name] && len(names) < maxTNEFNames {
				seen[*p.Name] = true
				names = append(names, *p.Name)
			}
		}
	}
	return names
}

// attachmentProps returns each attachment's property list.
func attachmentProps(t *tnef.Message) [][]tnef.Prop {
	out := make([][]tnef.Prop, 0, len(t.Attachments))
	for _, a := range t.Attachments {
		out = append(out, a.Props)
	}
	return out
}

// localTag returns the store tag of a decoded property, false for a named
// property the store did not name or a property a sender must not set.
func (c tnefContext) localTag(p tnef.Prop) (mapi.PropTag, bool) {
	if p.Name != nil {
		id := c.ids[*p.Name]
		return mapi.MakeTag(id, p.Tag.Type()), id != 0
	}
	return p.Tag, transmittable(p.Tag)
}

// transmittable reports whether a sender may set a property. The store computes
// the 0x0E00-0x0FFF range, and 0x6600-0x67FF and 0x7C00-0x7FFF are provider
// ranges: hermEX keeps the verbatim bytes it serves there, so a stream that set
// one would choose what a client is served.
func transmittable(tag mapi.PropTag) bool {
	id := tag.ID()
	switch {
	case id >= 0x0E00 && id <= 0x0FFF, id >= 0x6600 && id <= 0x67FF, id >= 0x7C00 && id <= 0x7FFF:
		return false
	case id >= 0x8000:
		return false
	}
	return tag.WithType(mapi.PtUnicode) != mapi.PrTransportMessageHeaders
}

// setProps copies decoded properties onto a bag. Without override a property the
// bag already holds is kept, and the class replaces only the generic IPM.Note.
func (c tnefContext) setProps(bag *mapi.PropertyValues, props []tnef.Prop, override bool) {
	for _, p := range props {
		tag, ok := c.localTag(p)
		if !ok || tag.Type() == mapi.PtObject {
			continue
		}
		if !override && keepsOwnValue(*bag, tag) {
			continue
		}
		bag.Set(tag, p.Value)
	}
}

// keepsOwnValue reports whether a bag's value outranks the stream's: any value it
// holds does, except the generic IPM.Note class, which only says the MIME wrapper
// named no class.
func keepsOwnValue(bag mapi.PropertyValues, tag mapi.PropTag) bool {
	if tag == mapi.PrMessageClass && propString(bag, tag) == "IPM.Note" {
		return false
	}
	return bagHas(bag, tag)
}

// bagHas reports whether a bag holds tag in either string representation.
func bagHas(bag mapi.PropertyValues, tag mapi.PropTag) bool {
	_, ok := bag.GetAnyCharset(tag)
	return ok
}

// addRecipients appends the stream's recipient rows.
func (c tnefContext) addRecipients(msg *Message, t *tnef.Message) {
	for _, row := range t.Recipients {
		var r mapi.PropertyValues
		c.setProps(&r, row, true)
		if len(r) > 0 {
			msg.Recipients = append(msg.Recipients, r)
		}
	}
}

// attachment builds one attachment from its decoded properties. An attached
// message becomes an embedded message stored as RFC 5322 bytes, the form every
// other embedded message has here.
func (c tnefContext) attachment(a tnef.Attachment) Attachment {
	var att Attachment
	c.setProps(&att.Props, a.Props, true)
	for _, p := range a.Props {
		if o, ok := p.Value.(tnef.Object); ok && p.Tag == mapi.PrAttachDataObj {
			c.embed(&att, o)
		}
	}
	// An attached message whose stream did not decode has no data to open as one.
	if method, _ := propInt32(att.Props, mapi.PrAttachMethod); method == 0 ||
		(method == mapi.AttachEmbeddedMsg && !att.Props.Has(mapi.PrAttachDataBin)) {
		att.Props.Set(mapi.PrAttachMethod, int32(mapi.AttachByValue))
	}
	if !att.Props.Has(mapi.PrAttachMimeTag) {
		att.Props.Set(mapi.PrAttachMimeTag, attachmentType(att.Props))
	}
	for _, t := range []mapi.PropTag{mapi.PrCreationTime, mapi.PrLastModificationTime} {
		if !att.Props.Has(t) {
			att.Props.Set(t, c.stamp)
		}
	}
	return att
}

// embed stores an attached message's stream as its RFC 5322 rendering. A stream
// that does not decode leaves the attachment without data rather than failing the
// message.
func (c tnefContext) embed(att *Attachment, o tnef.Object) {
	inner, err := tnef.DecodeEmbedded(o, decodeANSI, c.depth)
	if err != nil {
		return
	}
	sub := tnefContext{ids: map[mapi.PropertyName]uint16{}, stamp: c.stamp, depth: c.depth + 1}
	m := &Message{}
	sub.setProps(&m.Props, inner.Props, true)
	sub.addRecipients(m, inner)
	for _, ia := range inner.Attachments {
		m.Attachments = append(m.Attachments, sub.attachment(ia))
	}
	raw, err := Export(m, Options{})
	if err != nil {
		return
	}
	att.Props.Set(mapi.PrAttachMethod, int32(mapi.AttachEmbeddedMsg))
	att.Props.Set(mapi.PrAttachMimeTag, "message/rfc822")
	att.Props.Set(mapi.PrAttachDataBin, raw)
}

// attachmentType guesses an attachment's media type from its file name.
func attachmentType(props mapi.PropertyValues) string {
	if t := stdmime.TypeByExtension(filenameExtension(propString(props, mapi.PrAttachLongFilename))); t != "" {
		return t
	}
	return "application/octet-stream"
}
