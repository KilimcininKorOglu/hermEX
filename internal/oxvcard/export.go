package oxvcard

import (
	"encoding/base64"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// Export renders an IPM.Contact message as a vCard 4.0. Named properties (email
// slots, work address, IM, has-picture, the preserved UID) are resolved through
// opt.Resolver with create=false: a property never stored simply does not
// appear.
func Export(msg *oxcmail.Message, opt Options) ([]byte, error) {
	named, err := namedTags(opt, false)
	if err != nil {
		return nil, err
	}
	uidTag, err := resolveOne(opt, nameVCardUID, mapi.PtUnicode, false)
	if err != nil {
		return nil, err
	}
	catTag, err := resolveOne(opt, mapi.NameKeywords, mapi.PtMvUnicode, false)
	if err != nil {
		return nil, err
	}
	p := &msg.Props
	b := &builder{}

	b.add("BEGIN:VCARD")
	b.add("VERSION:4.0")

	// FN is mandatory; fall back to the assembled name or a placeholder.
	fn := getStr(p, mapi.PrDisplayName)
	if fn == "" {
		fn = strings.TrimSpace(getStr(p, mapi.PrGivenName) + " " + getStr(p, mapi.PrSurname))
	}
	if fn == "" {
		fn = "Unknown"
	}
	b.line("FN", fn)

	if n := structured(
		getStr(p, mapi.PrSurname), getStr(p, mapi.PrGivenName), getStr(p, mapi.PrMiddleName),
		getStr(p, mapi.PrDisplayNamePrefix), getStr(p, mapi.PrGeneration),
	); n != "" {
		b.add("N:" + n)
	}
	addLine(b, "NICKNAME", getStr(p, mapi.PrNickname))
	addLine(b, "TITLE", getStr(p, mapi.PrTitle))
	addLine(b, "ROLE", getStr(p, mapi.PrProfession))
	if org := structured(getStr(p, mapi.PrCompanyName), getStr(p, mapi.PrDepartmentName)); org != "" {
		b.add("ORG:" + org)
	}
	addLine(b, "NOTE", getStr(p, mapi.PrBody))
	if v, ok := p.Get(mapi.PrBirthday); ok {
		if nt, ok := v.(uint64); ok {
			b.line("BDAY", mapi.NTTimeToUnix(nt).UTC().Format("2006-01-02"))
		}
	}

	exportPhones(b, p)
	exportAddresses(b, p, named)
	exportEmails(b, p, named)
	if tag, ok := named[mapi.NameInstantMessagingAddress]; ok {
		addLine(b, "IMPP", getStr(p, tag))
	}
	exportURLs(b, p)
	exportCategories(b, p, catTag)
	exportPhoto(b, msg)
	if uidTag != 0 {
		addLine(b, "UID", getStr(p, uidTag))
	}

	b.add("END:VCARD")
	return b.buf.Bytes(), nil
}

// phoneType pairs a telephone proptag with the TYPE parameter Export emits.
var phoneType = []struct {
	tag mapi.PropTag
	typ string
}{
	{mapi.PrMobileTelephoneNumber, "cell"},
	{mapi.PrHomeTelephoneNumber, "home"},
	{mapi.PrBusinessTelephoneNumber, "work"},
	{mapi.PrBusinessFaxNumber, "fax,work"},
	{mapi.PrHomeFaxNumber, "fax,home"},
	{mapi.PrPagerTelephoneNumber, "pager"},
	{mapi.PrCarTelephoneNumber, "car"},
	{mapi.PrOtherTelephoneNumber, "voice"},
}

// exportPhones emits one TEL line per populated telephone property.
func exportPhones(b *builder, p *mapi.PropertyValues) {
	for _, pt := range phoneType {
		if v := getStr(p, pt.tag); v != "" {
			b.add("TEL;TYPE=" + pt.typ + ":" + escapeValue(v))
		}
	}
}

// exportAddresses emits the home, work, and other ADR lines (PO box; extended;
// street; city; region; postal; country) when any component is set.
func exportAddresses(b *builder, p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag) {
	emit := func(typ, street, city, region, postal, country, pobox string) {
		adr := structured(pobox, "", street, city, region, postal, country)
		if strings.Trim(adr, ";") != "" {
			b.add("ADR;TYPE=" + typ + ":" + adr)
		}
	}
	emit("home",
		getStr(p, mapi.PrHomeAddressStreet), getStr(p, mapi.PrHomeAddressCity),
		getStr(p, mapi.PrHomeAddressStateOrProvince), getStr(p, mapi.PrHomeAddressPostalCode),
		getStr(p, mapi.PrHomeAddressCountry), getStr(p, mapi.PrHomeAddressPostOfficeBox))
	emit("work",
		namedStr(p, named, mapi.NameWorkAddressStreet), namedStr(p, named, mapi.NameWorkAddressCity),
		namedStr(p, named, mapi.NameWorkAddressState), namedStr(p, named, mapi.NameWorkAddressPostalCode),
		namedStr(p, named, mapi.NameWorkAddressCountry), namedStr(p, named, mapi.NameWorkAddressPostOfficeBox))
	emit("other",
		getStr(p, mapi.PrOtherAddressStreet), getStr(p, mapi.PrOtherAddressCity),
		getStr(p, mapi.PrOtherAddressStateOrProvince), getStr(p, mapi.PrOtherAddressPostalCode),
		getStr(p, mapi.PrOtherAddressCountry), getStr(p, mapi.PrOtherAddressPostOfficeBox))
}

// exportEmails emits one EMAIL line per populated contact email slot.
func exportEmails(b *builder, p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag) {
	for i := 0; i < 3; i++ {
		if addr := namedStr(p, named, emailSlot[i][0]); addr != "" {
			b.line("EMAIL", addr)
		}
	}
}

// exportURLs emits business and personal home-page URLs.
func exportURLs(b *builder, p *mapi.PropertyValues) {
	if v := getStr(p, mapi.PrBusinessHomePage); v != "" {
		b.add("URL;TYPE=work:" + escapeValue(v))
	}
	if v := getStr(p, mapi.PrPersonalHomePage); v != "" {
		b.add("URL;TYPE=home:" + escapeValue(v))
	}
}

// exportCategories emits the CATEGORIES line from the multivalue keywords prop.
func exportCategories(b *builder, p *mapi.PropertyValues, catTag mapi.PropTag) {
	if catTag == 0 {
		return
	}
	v, ok := p.Get(catTag)
	if !ok {
		return
	}
	cats, ok := v.([]string)
	if !ok || len(cats) == 0 {
		return
	}
	escaped := make([]string, len(cats))
	for i, c := range cats {
		escaped[i] = escapeValue(c)
	}
	b.add("CATEGORIES:" + strings.Join(escaped, ","))
}

// exportPhoto emits the first attachment as an inline base64 PHOTO, sniffing the
// image type from the data.
func exportPhoto(b *builder, msg *oxcmail.Message) {
	if len(msg.Attachments) == 0 {
		return
	}
	v, ok := msg.Attachments[0].Props.Get(mapi.PrAttachDataBin)
	if !ok {
		return
	}
	data, ok := v.([]byte)
	if !ok || len(data) == 0 {
		return
	}
	b.add("PHOTO:data:" + sniffImage(data) + ";base64," + base64.StdEncoding.EncodeToString(data))
}

// getStr returns a string-valued property, or "".
func getStr(p *mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := p.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// namedStr returns a named string-valued property, or "".
func namedStr(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName) string {
	if tag, ok := named[name]; ok {
		return getStr(p, tag)
	}
	return ""
}

// addLine emits a simple line only when the value is non-empty.
func addLine(b *builder, name, value string) {
	if value != "" {
		b.line(name, value)
	}
}

// structured joins components into an escaped ";"-separated value, or "" when
// every component is empty.
func structured(components ...string) string {
	any := false
	escaped := make([]string, len(components))
	for i, c := range components {
		escaped[i] = escapeValue(c)
		if c != "" {
			any = true
		}
	}
	if !any {
		return ""
	}
	return strings.Join(escaped, ";")
}

// sniffImage returns a MIME type for common image payloads, defaulting to JPEG.
func sniffImage(data []byte) string {
	switch {
	case len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G':
		return "image/png"
	case len(data) >= 4 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F':
		return "image/gif"
	default:
		return "image/jpeg"
	}
}
