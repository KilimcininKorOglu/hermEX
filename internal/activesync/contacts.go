package activesync

import (
	"strconv"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
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
	names := make([]mapi.PropertyName, len(contactNamedFields))
	for i, f := range contactNamedFields {
		names[i] = f.name
	}
	ids, err := st.GetNamedPropIDs(false, names)
	if err != nil {
		return nil, err
	}

	tags := []mapi.PropTag{mapi.PrBirthday, mapi.PrWeddingAnniversary, mapi.PrChildrensNames}
	for _, f := range contactStringFields {
		tags = append(tags, f.prop)
	}
	namedTag := make([]mapi.PropTag, len(contactNamedFields))
	for i := range contactNamedFields {
		if ids[i] != 0 {
			namedTag[i] = mapi.MakeTag(ids[i], mapi.PtUnicode)
			tags = append(tags, namedTag[i])
		}
	}
	// Categories are the shared message keywords (a multivalue named property), the
	// same store CATEGORIES vCard import/export uses.
	var keywordsTag mapi.PropTag
	if kid, err := st.GetNamedPropIDs(false, []mapi.PropertyName{mapi.NameKeywords}); err == nil && kid[0] != 0 {
		keywordsTag = mapi.MakeTag(kid[0], mapi.PtMvUnicode)
		tags = append(tags, keywordsTag)
	}
	pv, err := st.GetMessageProperties(objectID, tags...)
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
		if namedTag[i] != 0 {
			if s := contactStr(pv, namedTag[i]); s != "" {
				data.Children = append(data.Children, wbxml.Str(f.tag, s))
			}
		}
	}
	if t, ok := ntTimeProp(pv, mapi.PrBirthday); ok {
		data.Children = append(data.Children, wbxml.Str(wbxml.CBirthday, t.UTC().Format(easContactDate)))
	}
	if t, ok := ntTimeProp(pv, mapi.PrWeddingAnniversary); ok {
		data.Children = append(data.Children, wbxml.Str(wbxml.CAnniversary, t.UTC().Format(easContactDate)))
	}
	if v, ok := pv.Get(mapi.PrChildrensNames); ok {
		if kids, ok := v.([]string); ok && len(kids) > 0 {
			var nodes []*wbxml.Node
			for _, k := range kids {
				nodes = append(nodes, wbxml.Str(wbxml.CChild, k))
			}
			data.Children = append(data.Children, wbxml.Elem(wbxml.CChildren, nodes...))
		}
	}
	if keywordsTag != 0 {
		if v, ok := pv.Get(keywordsTag); ok {
			if cats, ok := v.([]string); ok && len(cats) > 0 {
				var nodes []*wbxml.Node
				for _, c := range cats {
					nodes = append(nodes, wbxml.Str(wbxml.CCategory, c))
				}
				data.Children = append(data.Children, wbxml.Elem(wbxml.CCategories, nodes...))
			}
		}
	}
	return data, nil
}

// parseContactItem decodes a client's contact ApplicationData into MAPI properties.
func parseContactItem(st *objectstore.Store, data *wbxml.Node) (mapi.PropertyValues, error) {
	names := make([]mapi.PropertyName, len(contactNamedFields))
	for i, f := range contactNamedFields {
		names[i] = f.name
	}
	ids, err := st.GetNamedPropIDs(true, names)
	if err != nil {
		return nil, err
	}

	var props mapi.PropertyValues
	for _, f := range contactStringFields {
		if s := data.ChildText(f.tag); s != "" {
			props = append(props, mapi.TaggedPropVal{Tag: f.prop, Value: s})
		}
	}
	for i, f := range contactNamedFields {
		if ids[i] == 0 {
			continue
		}
		if s := data.ChildText(f.tag); s != "" {
			props = append(props, mapi.TaggedPropVal{Tag: mapi.MakeTag(ids[i], mapi.PtUnicode), Value: s})
		}
	}
	// Each email carries a display name and an SMTP address type alongside its
	// address, matching the shape vCard import writes, so a contact created here is
	// identical in the store to one created over CardDAV and stays recognizable to
	// MAPI/EWS clients reading the same object.
	emailDT, err := st.GetNamedPropIDs(true, []mapi.PropertyName{
		mapi.NameEmail1DisplayName, mapi.NameEmail1AddressType,
		mapi.NameEmail2DisplayName, mapi.NameEmail2AddressType,
		mapi.NameEmail3DisplayName, mapi.NameEmail3AddressType,
	})
	if err != nil {
		return nil, err
	}
	for slot, tag := range []wbxml.Tag{wbxml.CEmail1Address, wbxml.CEmail2Address, wbxml.CEmail3Address} {
		addr := data.ChildText(tag)
		if addr == "" {
			continue
		}
		if id := emailDT[slot*2]; id != 0 {
			props = append(props, mapi.TaggedPropVal{Tag: mapi.MakeTag(id, mapi.PtUnicode), Value: addr})
		}
		if id := emailDT[slot*2+1]; id != 0 {
			props = append(props, mapi.TaggedPropVal{Tag: mapi.MakeTag(id, mapi.PtUnicode), Value: "SMTP"})
		}
	}
	if b := data.ChildText(wbxml.CBirthday); b != "" {
		if t, err := time.Parse(easContactDate, b); err == nil {
			props = append(props, mapi.TaggedPropVal{Tag: mapi.PrBirthday, Value: mapi.UnixToNTTime(t)})
		}
	}
	if a := data.ChildText(wbxml.CAnniversary); a != "" {
		if t, err := time.Parse(easContactDate, a); err == nil {
			props = append(props, mapi.TaggedPropVal{Tag: mapi.PrWeddingAnniversary, Value: mapi.UnixToNTTime(t)})
		}
	}
	if kids := data.Child(wbxml.CChildren); kids != nil {
		var names []string
		for _, c := range kids.Children {
			if c.Tag == wbxml.CChild && c.Text != "" {
				names = append(names, c.Text)
			}
		}
		if len(names) > 0 {
			props = append(props, mapi.TaggedPropVal{Tag: mapi.PrChildrensNames, Value: names})
		}
	}
	if cats := data.Child(wbxml.CCategories); cats != nil {
		var vals []string
		for _, c := range cats.Children {
			if c.Tag == wbxml.CCategory && c.Text != "" {
				vals = append(vals, c.Text)
			}
		}
		if len(vals) > 0 {
			if kid, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameKeywords}); err == nil && kid[0] != 0 {
				props = append(props, mapi.TaggedPropVal{Tag: mapi.MakeTag(kid[0], mapi.PtMvUnicode), Value: vals})
			}
		}
	}
	return props, nil
}

// applyContactClientCommands applies a device's Add/Change/Delete commands to the
// Contacts folder, mirroring the calendar path: an Add creates the contact and
// returns the assigned server id, a Change rewrites scalar fields without bumping the
// change number (so it is not echoed back), and a Delete removes the contact.
func applyContactClientCommands(st *objectstore.Store, cstate *collectionState, c *wbxml.Node) []*wbxml.Node {
	cmds := c.Child(wbxml.ASCommands)
	if cmds == nil {
		return nil
	}
	var responses []*wbxml.Node
	added := map[string]bool{}
	for _, cmd := range cmds.Children {
		switch cmd.Tag {
		case wbxml.ASAdd:
			clientID := cmd.ChildText(wbxml.ASClientID)
			data := cmd.Child(wbxml.ASData)
			if clientID == "" || data == nil {
				continue
			}
			props, err := parseContactItem(st, data)
			if err != nil {
				continue
			}
			props = append(props, mapi.TaggedPropVal{Tag: mapi.PrMessageClass, Value: "IPM.Contact"})
			id, err := st.CreateMessage(int64(mapi.PrivateFIDContacts), &oxcmail.Message{Props: props})
			if err != nil {
				continue
			}
			sid := strconv.FormatInt(id, 10)
			added[sid] = true
			responses = append(responses, wbxml.Elem(wbxml.ASAdd,
				wbxml.Str(wbxml.ASClientID, clientID),
				wbxml.Str(wbxml.ASServerID, sid),
				wbxml.Str(wbxml.ASStatus, strconv.Itoa(syncStatusOK))))
		case wbxml.ASChange:
			id, err := strconv.ParseInt(cmd.ChildText(wbxml.ASServerID), 10, 64)
			if err != nil {
				continue
			}
			data := cmd.Child(wbxml.ASData)
			if data == nil {
				continue
			}
			props, err := parseContactItem(st, data)
			if err != nil || len(props) == 0 {
				continue
			}
			_ = st.SetMessageProperties(id, props)
		case wbxml.ASDelete:
			sid := cmd.ChildText(wbxml.ASServerID)
			id, err := strconv.ParseInt(sid, 10, 64)
			if err != nil {
				continue
			}
			if st.DeleteObject(id) == nil {
				delete(cstate.Items, sid)
			}
		}
	}
	// Fold the just-added contacts into the snapshot so objectChanges does not echo
	// them back as server adds to the device that just created them.
	if len(added) > 0 {
		if objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDContacts)); err == nil {
			for _, o := range objs {
				if sid := strconv.FormatInt(o.ID, 10); added[sid] {
					cstate.Items[sid] = int64(o.ChangeNumber)
				}
			}
		}
	}
	return responses
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

// isObjectFolder reports whether a folder's items live in the object store and are
// versioned by change number (calendar, contacts, tasks) rather than the IMAP index.
func isObjectFolder(folderID int64) bool {
	switch folderID {
	case int64(mapi.PrivateFIDCalendar), int64(mapi.PrivateFIDContacts),
		int64(mapi.PrivateFIDTasks), int64(mapi.PrivateFIDNotes):
		return true
	}
	return false
}

// objectAppData returns the data-class renderer for an object folder's items.
func objectAppData(folderID int64) func(*objectstore.Store, int64) (*wbxml.Node, error) {
	switch folderID {
	case int64(mapi.PrivateFIDContacts):
		return contactAppData
	case int64(mapi.PrivateFIDTasks):
		return taskAppData
	case int64(mapi.PrivateFIDNotes):
		return noteAppData
	default:
		return calendarAppData
	}
}

// applyObjectClientCommands dispatches a device's commands to the right object
// folder's apply path.
func applyObjectClientCommands(st *objectstore.Store, folderID int64, cstate *collectionState, c *wbxml.Node) []*wbxml.Node {
	switch folderID {
	case int64(mapi.PrivateFIDContacts):
		return applyContactClientCommands(st, cstate, c)
	case int64(mapi.PrivateFIDTasks):
		return applyTaskClientCommands(st, cstate, c)
	case int64(mapi.PrivateFIDNotes):
		return applyNoteClientCommands(st, cstate, c)
	default:
		return applyCalendarClientCommands(st, cstate, c)
	}
}
