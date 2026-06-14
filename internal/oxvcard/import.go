package oxvcard

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

var (
	errNoCard  = errors.New("oxvcard: no BEGIN:VCARD block")
	errVersion = errors.New("oxvcard: unsupported vCard version (need 3.0 or 4.0)")
)

// nameVCardUID preserves the vCard UID as a contact named property (a neutral
// string name in the public-strings namespace) so the DAV layer and Export can
// round-trip a card's stable identity.
var nameVCardUID = mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsPublicStrings, Name: "VCardUID"}

// Import parses a vCard into an IPM.Contact message. It accepts vCard 3.0 and
// 4.0 and rejects 2.1. Named properties (email slots, work address, file-as, IM
// address, has-picture, the preserved UID) are resolved through opt.Resolver.
func Import(raw []byte, opt Options) (*oxcmail.Message, error) {
	card, err := parseVCard(raw)
	if err != nil {
		return nil, err
	}
	if v := card.version(); v != "3.0" && v != "4.0" {
		return nil, errVersion
	}
	named, err := namedTags(opt, true)
	if err != nil {
		return nil, err
	}
	uidTag, err := resolveOne(opt, nameVCardUID, mapi.PtUnicode, true)
	if err != nil {
		return nil, err
	}

	msg := &oxcmail.Message{}
	p := &msg.Props
	p.Set(mapi.PrMessageClass, "IPM.Contact")

	if l := card.get("FN"); l != nil {
		p.Set(mapi.PrDisplayName, l.text())
	}
	if l := card.get("N"); l != nil {
		setIf(p, mapi.PrSurname, l.component(0))
		setIf(p, mapi.PrGivenName, l.component(1))
		setIf(p, mapi.PrMiddleName, l.component(2))
		setIf(p, mapi.PrDisplayNamePrefix, l.component(3))
		setIf(p, mapi.PrGeneration, l.component(4))
	}
	if l := card.get("NICKNAME"); l != nil {
		setIf(p, mapi.PrNickname, l.text())
	}
	if l := card.get("TITLE"); l != nil {
		setIf(p, mapi.PrTitle, l.text())
	}
	if l := card.get("ROLE"); l != nil {
		setIf(p, mapi.PrProfession, l.text())
	}
	if l := card.get("ORG"); l != nil {
		setIf(p, mapi.PrCompanyName, l.component(0))
		setIf(p, mapi.PrDepartmentName, l.component(1))
	}
	if l := card.get("NOTE"); l != nil {
		setIf(p, mapi.PrBody, l.text())
	}
	if l := card.get("BDAY"); l != nil {
		if nt, ok := parseBirthday(l.text()); ok {
			p.Set(mapi.PrBirthday, nt)
		}
	}
	for _, l := range card.all("URL") {
		if l.hasType("home") {
			setIf(p, mapi.PrPersonalHomePage, l.text())
		} else {
			setIf(p, mapi.PrBusinessHomePage, l.text())
		}
	}
	for _, l := range card.all("TEL") {
		if tag := telTag(l.types()); tag != 0 {
			setIf(p, tag, l.text())
		}
	}
	importAddresses(p, card, named)
	importEmails(p, card, named)
	if l := card.get("IMPP"); l != nil {
		setNamed(p, named, mapi.NameInstantMessagingAddress, l.text())
	}
	if cats := importCategories(card); len(cats) > 0 {
		if tag, err := resolveOne(opt, mapi.NameKeywords, mapi.PtMvUnicode, true); err == nil && tag != 0 {
			p.Set(tag, cats)
		}
	}
	if uidTag != 0 {
		uid := ""
		if l := card.get("UID"); l != nil {
			uid = strings.TrimSpace(l.text())
		}
		if uid == "" {
			uid = generatedUID(card)
		}
		p.Set(uidTag, uid)
	}
	importPhoto(msg, card, named)

	return msg, nil
}

// setIf sets a string property only when the value is non-empty.
func setIf(p *mapi.PropertyValues, tag mapi.PropTag, v string) {
	if v != "" {
		p.Set(tag, v)
	}
}

// setNamed sets a named string property when its tag resolved and v is non-empty.
func setNamed(p *mapi.PropertyValues, named map[mapi.PropertyName]mapi.PropTag, name mapi.PropertyName, v string) {
	if tag, ok := named[name]; ok {
		setIf(p, tag, v)
	}
}

// telTag maps a TEL's TYPE set to the matching telephone proptag, most specific
// first (a work fax is a fax, not a work voice number). Returns 0 to skip.
func telTag(types []string) mapi.PropTag {
	has := func(t string) bool {
		for _, v := range types {
			if v == t {
				return true
			}
		}
		return false
	}
	switch {
	case has("fax") && has("home"):
		return mapi.PrHomeFaxNumber
	case has("fax"):
		return mapi.PrBusinessFaxNumber
	case has("cell"), has("mobile"):
		return mapi.PrMobileTelephoneNumber
	case has("pager"):
		return mapi.PrPagerTelephoneNumber
	case has("car"):
		return mapi.PrCarTelephoneNumber
	case has("home"):
		return mapi.PrHomeTelephoneNumber
	case has("work"), has("voice"):
		return mapi.PrBusinessTelephoneNumber
	default:
		return mapi.PrOtherTelephoneNumber
	}
}

// importAddresses maps ADR lines (components PO box; ext; street; city; region;
// postal; country) to the home (PidTag), work (named), or other (PidTag) sets.
func importAddresses(p *mapi.PropertyValues, card *vcard, named map[mapi.PropertyName]mapi.PropTag) {
	for _, l := range card.all("ADR") {
		street, city, region := l.component(2), l.component(3), l.component(4)
		postal, country, pobox := l.component(5), l.component(6), l.component(0)
		switch {
		case l.hasType("work"):
			setNamed(p, named, mapi.NameWorkAddressStreet, street)
			setNamed(p, named, mapi.NameWorkAddressCity, city)
			setNamed(p, named, mapi.NameWorkAddressState, region)
			setNamed(p, named, mapi.NameWorkAddressPostalCode, postal)
			setNamed(p, named, mapi.NameWorkAddressCountry, country)
			setNamed(p, named, mapi.NameWorkAddressPostOfficeBox, pobox)
		case l.hasType("home"):
			setIf(p, mapi.PrHomeAddressStreet, street)
			setIf(p, mapi.PrHomeAddressCity, city)
			setIf(p, mapi.PrHomeAddressStateOrProvince, region)
			setIf(p, mapi.PrHomeAddressPostalCode, postal)
			setIf(p, mapi.PrHomeAddressCountry, country)
			setIf(p, mapi.PrHomeAddressPostOfficeBox, pobox)
		default:
			setIf(p, mapi.PrOtherAddressStreet, street)
			setIf(p, mapi.PrOtherAddressCity, city)
			setIf(p, mapi.PrOtherAddressStateOrProvince, region)
			setIf(p, mapi.PrOtherAddressPostalCode, postal)
			setIf(p, mapi.PrOtherAddressCountry, country)
			setIf(p, mapi.PrOtherAddressPostOfficeBox, pobox)
		}
	}
}

// emailSlot names the address/display/type named properties for slots 1..3.
var emailSlot = [3][3]mapi.PropertyName{
	{mapi.NameEmail1Address, mapi.NameEmail1DisplayName, mapi.NameEmail1AddressType},
	{mapi.NameEmail2Address, mapi.NameEmail2DisplayName, mapi.NameEmail2AddressType},
	{mapi.NameEmail3Address, mapi.NameEmail3DisplayName, mapi.NameEmail3AddressType},
}

// importEmails maps up to three EMAIL lines into the three contact email slots,
// each carrying the address, a display name (the FN), and the SMTP address type.
func importEmails(p *mapi.PropertyValues, card *vcard, named map[mapi.PropertyName]mapi.PropTag) {
	display := ""
	if l := card.get("FN"); l != nil {
		display = l.text()
	}
	emails := card.all("EMAIL")
	for i := 0; i < len(emails) && i < 3; i++ {
		addr := strings.TrimSpace(emails[i].text())
		if addr == "" {
			continue
		}
		setNamed(p, named, emailSlot[i][0], addr)
		setNamed(p, named, emailSlot[i][1], display)
		setNamed(p, named, emailSlot[i][2], "SMTP")
	}
}

// importCategories returns the merged CATEGORIES values (comma-separated lists
// across one or more lines).
func importCategories(card *vcard) []string {
	var out []string
	for _, l := range card.all("CATEGORIES") {
		for _, part := range splitEscaped(l.value, ',') {
			if v := strings.TrimSpace(unescapeValue(part)); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// importPhoto decodes an inline base64 PHOTO into a contact-photo attachment and
// sets the has-picture flag. A non-inline (URI) photo is left out.
func importPhoto(msg *oxcmail.Message, card *vcard, named map[mapi.PropertyName]mapi.PropTag) {
	l := card.get("PHOTO")
	if l == nil {
		return
	}
	data, ok := decodePhoto(l)
	if !ok {
		return
	}
	att := oxcmail.Attachment{Props: mapi.PropertyValues{}}
	att.Props.Set(mapi.PrAttachMethod, int32(mapi.AttachByValue))
	att.Props.Set(mapi.PrAttachDataBin, data)
	msg.Attachments = append(msg.Attachments, att)
	if tag, ok := named[mapi.NameHasPicture]; ok {
		msg.Props.Set(tag, true)
	}
}

// decodePhoto extracts raw image bytes from a PHOTO line whose value is inline
// base64 — either a 4.0 "data:" URI or a 3.0 ENCODING=b value.
func decodePhoto(l *vline) ([]byte, bool) {
	v := strings.TrimSpace(l.value)
	if strings.HasPrefix(strings.ToLower(v), "data:") {
		if i := strings.Index(v, ","); i >= 0 {
			v = v[i+1:]
		}
	}
	enc := strings.ToLower(strings.Join(l.params["ENCODING"], ""))
	inline := strings.HasPrefix(strings.ToLower(strings.TrimSpace(l.value)), "data:") || enc == "b" || enc == "base64"
	if !inline {
		return nil, false
	}
	data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(v), ""))
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// parseBirthday parses a vCard BDAY date (YYYYMMDD or YYYY-MM-DD, optionally with
// a time suffix) into a FILETIME, dropping any time-of-day.
func parseBirthday(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "T "); i >= 0 {
		s = s[:i]
	}
	for _, layout := range []string{"2006-01-02", "20060102"} {
		if t, err := time.Parse(layout, s); err == nil {
			return mapi.UnixToNTTime(t.UTC()), true
		}
	}
	return 0, false
}

// resolveOne resolves a single named property to its full proptag.
func resolveOne(opt Options, name mapi.PropertyName, typ mapi.PropType, create bool) (mapi.PropTag, error) {
	ids, err := opt.Resolver(create, []mapi.PropertyName{name})
	if err != nil {
		return 0, err
	}
	if ids[0] == 0 {
		return 0, nil
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(typ)), nil
}

// generatedUID derives a deterministic UID for a card that carries none, so the
// same input yields the same identity. It is based on the display name.
func generatedUID(card *vcard) string {
	base := "contact"
	if l := card.get("FN"); l != nil {
		base = l.text()
	}
	return "hermex-" + strings.Map(func(r rune) rune {
		if r == ' ' {
			return '-'
		}
		return r
	}, strings.ToLower(strings.TrimSpace(base)))
}
