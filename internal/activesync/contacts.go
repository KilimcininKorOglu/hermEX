package activesync

import (
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// MS-ASCONTACTS: a stored IPM.Contact maps to and from the AirSync Contacts class.
// The scalar string fields are a fixed table of (WBXML tag <-> MAPI property); the
// email and business-address fields resolve to named properties; birthday and
// anniversary are dates; children is multivalued.

// contactStringFields maps each scalar contact field to its direct MAPI property.
var contactStringFields = []struct {
	tag  wbxml.Tag
	prop mapi.PropTag
}{
	{wbxml.CFirstName, mapi.PrGivenName},
	{wbxml.CMiddleName, mapi.PrMiddleName},
	{wbxml.CLastName, mapi.PrSurname},
	{wbxml.CTitle, mapi.PrDisplayNamePrefix},
	{wbxml.CSuffix, mapi.PrGeneration},
	{wbxml.CFileAs, mapi.PrDisplayName},
	{wbxml.CJobTitle, mapi.PrTitle},
	{wbxml.CCompanyName, mapi.PrCompanyName},
	{wbxml.CDepartment, mapi.PrDepartmentName},
	{wbxml.COfficeLocation, mapi.PrOfficeLocation},
	{wbxml.CAssistantName, mapi.PrAssistant},
	{wbxml.CSpouse, mapi.PrSpouseName},
	{wbxml.CWebPage, mapi.PrBusinessHomePage},
	{wbxml.CMobilePhoneNumber, mapi.PrMobileTelephoneNumber},
	{wbxml.CBusinessPhoneNumber, mapi.PrBusinessTelephoneNumber},
	{wbxml.CBusiness2PhoneNumber, mapi.PrBusiness2TelephoneNumber},
	{wbxml.CBusinessFaxNumber, mapi.PrBusinessFaxNumber},
	{wbxml.CHomePhoneNumber, mapi.PrHomeTelephoneNumber},
	{wbxml.CHome2PhoneNumber, mapi.PrHome2TelephoneNumber},
	{wbxml.CHomeFaxNumber, mapi.PrHomeFaxNumber},
	{wbxml.CPagerNumber, mapi.PrPagerTelephoneNumber},
	{wbxml.CCarPhoneNumber, mapi.PrCarTelephoneNumber},
	{wbxml.CRadioPhoneNumber, mapi.PrRadioTelephoneNumber},
	{wbxml.CAssistantPhoneNumber, mapi.PrAssistantTelephoneNumber},
	{wbxml.CHomeStreet, mapi.PrHomeAddressStreet},
	{wbxml.CHomeCity, mapi.PrHomeAddressCity},
	{wbxml.CHomeState, mapi.PrHomeAddressStateOrProvince},
	{wbxml.CHomePostalCode, mapi.PrHomeAddressPostalCode},
	{wbxml.CHomeCountry, mapi.PrHomeAddressCountry},
	{wbxml.COtherStreet, mapi.PrOtherAddressStreet},
	{wbxml.COtherCity, mapi.PrOtherAddressCity},
	{wbxml.COtherState, mapi.PrOtherAddressStateOrProvince},
	{wbxml.COtherPostalCode, mapi.PrOtherAddressPostalCode},
	{wbxml.COtherCountry, mapi.PrOtherAddressCountry},
}

// contactNamedFields maps each field backed by a named property: the three email
// addresses and the business (work) postal address.
var contactNamedFields = []struct {
	tag  wbxml.Tag
	name mapi.PropertyName
}{
	{wbxml.CEmail1Address, mapi.NameEmail1Address},
	{wbxml.CEmail2Address, mapi.NameEmail2Address},
	{wbxml.CEmail3Address, mapi.NameEmail3Address},
	{wbxml.CBusinessStreet, mapi.NameWorkAddressStreet},
	{wbxml.CBusinessCity, mapi.NameWorkAddressCity},
	{wbxml.CBusinessState, mapi.NameWorkAddressState},
	{wbxml.CBusinessPostalCode, mapi.NameWorkAddressPostalCode},
	{wbxml.CBusinessCountry, mapi.NameWorkAddressCountry},
}

// easContactDate is the MS-ASCONTACTS date-time format for Birthday/Anniversary.
const easContactDate = "2006-01-02T15:04:05.000Z"

// contactAppData builds the AirSync ApplicationData for a stored contact: every
// populated scalar, named (email/work-address), date, and multivalued field.
func contactAppData(st *objectstore.Store, objectID int64) (*wbxml.Node, error) {
	namedTag, err := contactNamedTags(st, false)
	if err != nil {
		return nil, err
	}
	// Categories are the shared message keywords (a multivalue named property), the
	// same store CATEGORIES vCard import/export uses.
	var keywordsTag mapi.PropTag
	if kid, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameKeywords}); err == nil && kid[0] != 0 {
		keywordsTag = mapi.MakeTag(kid[0], mapi.PtMvUnicode)
	}
	pv, err := st.GetMessageProperties(objectID, contactReadTags(namedTag, keywordsTag)...)
	if err != nil {
		return nil, err
	}

	data := wbxml.Elem(wbxml.ASData)
	for _, f := range contactStringFields {
		if s := contactStr(pv, f.prop); s != "" {
			data.Children = append(data.Children, wbxml.Str(f.tag, s))
		}
	}
	for i, f := range contactNamedFields {
		if s := contactStr(pv, namedTag[i]); namedTag[i] != 0 && s != "" {
			data.Children = append(data.Children, wbxml.Str(f.tag, s))
		}
	}
	appendContactDate(data, pv, mapi.PrBirthday, wbxml.CBirthday)
	appendContactDate(data, pv, mapi.PrWeddingAnniversary, wbxml.CAnniversary)
	appendContactList(data, pv, mapi.PrChildrensNames, wbxml.CChildren, wbxml.CChild)
	appendContactList(data, pv, keywordsTag, wbxml.CCategories, wbxml.CCategory)
	return data, nil
}

// contactNamedTags resolves the named-property tag of every contact field backed
// by one, in contactNamedFields order. An unresolved field gets tag 0.
func contactNamedTags(st *objectstore.Store, create bool) ([]mapi.PropTag, error) {
	names := make([]mapi.PropertyName, len(contactNamedFields))
	for i, f := range contactNamedFields {
		names[i] = f.name
	}
	ids, err := st.GetNamedPropIDs(create, names)
	if err != nil {
		return nil, err
	}
	tags := make([]mapi.PropTag, len(contactNamedFields))
	for i, id := range ids {
		if id != 0 {
			tags[i] = mapi.MakeTag(id, mapi.PtUnicode)
		}
	}
	return tags, nil
}

// contactReadTags lists every property contactAppData reads for one contact.
func contactReadTags(namedTag []mapi.PropTag, keywordsTag mapi.PropTag) []mapi.PropTag {
	tags := []mapi.PropTag{mapi.PrBirthday, mapi.PrWeddingAnniversary, mapi.PrChildrensNames}
	for _, f := range contactStringFields {
		tags = append(tags, f.prop)
	}
	for _, tag := range namedTag {
		if tag != 0 {
			tags = append(tags, tag)
		}
	}
	if keywordsTag != 0 {
		tags = append(tags, keywordsTag)
	}
	return tags
}

// appendContactDate emits one date field in the MS-ASCONTACTS format when the
// property is present.
func appendContactDate(data *wbxml.Node, pv mapi.PropertyValues, tag mapi.PropTag, elem wbxml.Tag) {
	if t, ok := ntTimeProp(pv, tag); ok {
		data.Children = append(data.Children, wbxml.Str(elem, t.UTC().Format(easContactDate)))
	}
}

// appendContactList emits one multivalued field as a container of item elements
// when the property holds at least one value.
func appendContactList(data *wbxml.Node, pv mapi.PropertyValues, tag mapi.PropTag, container, item wbxml.Tag) {
	if tag == 0 {
		return
	}
	v, ok := pv.Get(tag)
	if !ok {
		return
	}
	vals, ok := v.([]string)
	if !ok || len(vals) == 0 {
		return
	}
	nodes := make([]*wbxml.Node, 0, len(vals))
	for _, s := range vals {
		nodes = append(nodes, wbxml.Str(item, s))
	}
	data.Children = append(data.Children, wbxml.Elem(container, nodes...))
}

// parseContactItem decodes a client's contact ApplicationData into MAPI properties.
func parseContactItem(st *objectstore.Store, data *wbxml.Node) (mapi.PropertyValues, error) {
	namedTag, err := contactNamedTags(st, true)
	if err != nil {
		return nil, err
	}

	props := contactScalarProps(data, namedTag)
	emails, err := contactEmailProps(st, data)
	if err != nil {
		return nil, err
	}
	props = append(props, emails...)
	props = appendParsedDate(props, data, wbxml.CBirthday, mapi.PrBirthday)
	props = appendParsedDate(props, data, wbxml.CAnniversary, mapi.PrWeddingAnniversary)
	if kids := listChildText(data, wbxml.CChildren, wbxml.CChild); len(kids) > 0 {
		props = append(props, mapi.TaggedPropVal{Tag: mapi.PrChildrensNames, Value: kids})
	}
	return appendKeywords(st, props, listChildText(data, wbxml.CCategories, wbxml.CCategory)), nil
}

// contactScalarProps collects every populated single-valued contact field, both
// the ones on a direct property and the ones on a named property.
func contactScalarProps(data *wbxml.Node, namedTag []mapi.PropTag) mapi.PropertyValues {
	var props mapi.PropertyValues
	for _, f := range contactStringFields {
		if s := data.ChildText(f.tag); s != "" {
			props = append(props, mapi.TaggedPropVal{Tag: f.prop, Value: s})
		}
	}
	for i, f := range contactNamedFields {
		if s := data.ChildText(f.tag); namedTag[i] != 0 && s != "" {
			props = append(props, mapi.TaggedPropVal{Tag: namedTag[i], Value: s})
		}
	}
	return props
}

// appendKeywords appends the shared category keywords, the multivalue named
// property every protocol reads a category list from.
func appendKeywords(st *objectstore.Store, props mapi.PropertyValues, cats []string) mapi.PropertyValues {
	if len(cats) == 0 {
		return props
	}
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameKeywords})
	if err != nil || ids[0] == 0 {
		return props
	}
	return append(props, mapi.TaggedPropVal{Tag: mapi.MakeTag(ids[0], mapi.PtMvUnicode), Value: cats})
}

// contactEmailProps builds the display-name and address-type properties that
// accompany each populated email slot. They match the shape vCard import writes,
// so a contact created here is identical in the store to one created over CardDAV
// and stays recognizable to MAPI/EWS clients reading the same object.
func contactEmailProps(st *objectstore.Store, data *wbxml.Node) (mapi.PropertyValues, error) {
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameEmail1DisplayName, mapi.NameEmail1AddressType,
		mapi.NameEmail2DisplayName, mapi.NameEmail2AddressType,
		mapi.NameEmail3DisplayName, mapi.NameEmail3AddressType,
	})
	if err != nil {
		return nil, err
	}
	var props mapi.PropertyValues
	for slot, tag := range []wbxml.Tag{wbxml.CEmail1Address, wbxml.CEmail2Address, wbxml.CEmail3Address} {
		addr := data.ChildText(tag)
		if addr == "" {
			continue
		}
		props = appendNamedString(props, ids[slot*2], addr)
		props = appendNamedString(props, ids[slot*2+1], "SMTP")
	}
	return props, nil
}

// appendNamedString appends a unicode named property when its id resolved.
func appendNamedString(props mapi.PropertyValues, id uint16, value string) mapi.PropertyValues {
	if id == 0 {
		return props
	}
	return append(props, mapi.TaggedPropVal{Tag: mapi.MakeTag(id, mapi.PtUnicode), Value: value})
}

// appendParsedDate appends a date field parsed from the MS-ASCONTACTS format,
// skipping an absent or unparsable value.
func appendParsedDate(props mapi.PropertyValues, data *wbxml.Node, elem wbxml.Tag, tag mapi.PropTag) mapi.PropertyValues {
	s := data.ChildText(elem)
	if s == "" {
		return props
	}
	t, err := time.Parse(easContactDate, s)
	if err != nil {
		return props
	}
	return append(props, mapi.TaggedPropVal{Tag: tag, Value: mapi.UnixToNTTime(t)})
}

// listChildText collects the non-empty text of every item element inside one
// container element.
func listChildText(data *wbxml.Node, container, item wbxml.Tag) []string {
	node := data.Child(container)
	if node == nil {
		return nil
	}
	var out []string
	for _, c := range node.Children {
		if c.Tag == item && c.Text != "" {
			out = append(out, c.Text)
		}
	}
	return out
}

// parseContactProps decodes a device's contact ApplicationData and stamps the
// message class the store files a contact under.
func parseContactProps(st *objectstore.Store, data *wbxml.Node) (mapi.PropertyValues, error) {
	props, err := parseContactItem(st, data)
	if err != nil {
		return nil, err
	}
	return append(props, mapi.TaggedPropVal{Tag: mapi.PrMessageClass, Value: "IPM.Contact"}), nil
}

// contactStr returns a property's value as a string, or "" when absent.
func contactStr(pv mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := pv.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
