package ews

import (
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// extShape asks for a named string property under two equivalent spellings, a
// tagged integer property, and a named property the mailbox has never seen.
const extShape = `<ItemShape><BaseShape>Default</BaseShape><AdditionalProperties xmlns:t="` + nsTypes + `">` +
	`<t:ExtendedFieldURI DistinguishedPropertySetId="PublicStrings" PropertyName="hx-mark" PropertyType="String"/>` +
	`<t:ExtendedFieldURI PropertySetId="00020329-0000-0000-C000-000000000046" PropertyName="hx-mark" PropertyType="String"/>` +
	`<t:ExtendedFieldURI PropertyTag="0x6801" PropertyType="Integer"/>` +
	`<t:ExtendedFieldURI DistinguishedPropertySetId="PublicStrings" PropertyName="hx-absent" PropertyType="String"/>` +
	`</AdditionalProperties></ItemShape>`

// markMessage stores the named and the tagged property extShape asks for on the
// first Inbox message.
func markMessage(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	items, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil || len(items) == 0 {
		t.Fatalf("list inbox: %v (%d items)", err, len(items))
	}
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{{Kind: mapi.MnidString, GUID: mapi.PsPublicStrings, Name: "hx-mark"}})
	if err != nil {
		t.Fatal(err)
	}
	var props mapi.PropertyValues
	props.Set(mapi.MakeTag(ids[0], mapi.PtUnicode), "marked")
	props.Set(mapi.MakeTag(0x6801, mapi.PtLong), int32(42))
	if err := st.SetMessageProperties(items[0].ID, props); err != nil {
		t.Fatal(err)
	}
}

// checkExtended asserts a response carries the named property once, echoed with
// the distinguished set alone, the tagged property, and not the absent one.
func checkExtended(t *testing.T, op, out string) {
	t.Helper()
	if n := strings.Count(out, "<Value>marked</Value>"); n != 1 {
		t.Errorf("%s: the named property appears %d times, want once: %s", op, n, out)
	}
	if !strings.Contains(out, `DistinguishedPropertySetId="PublicStrings" PropertyName="hx-mark"`) || strings.Contains(out, "PropertySetId=\"00020329") {
		t.Errorf("%s: the named property is not echoed by its distinguished set alone: %s", op, out)
	}
	if !strings.Contains(out, `PropertyTag="0x6801" PropertyType="Integer"></ExtendedFieldURI><Value>42</Value>`) {
		t.Errorf("%s: the tagged property is missing: %s", op, out)
	}
	if strings.Contains(out, "hx-absent") {
		t.Errorf("%s: an absent property is served: %s", op, out)
	}
}

// TestItemShapeServesExtendedProperties proves FindItem, GetItem and
// SyncFolderItems return the extended properties the item shape asks for, each
// once per item whichever equivalent spellings named it, and never a property the
// item does not carry.
func TestItemShapeServesExtendedProperties(t *testing.T) {
	ts, dir := seededWithMessage(t, plainMessage)
	markMessage(t, dir)

	_, found := soapPost(t, ts, wrapRequest(`<FindItem Traversal="Shallow" xmlns="`+nsMessages+`">`+extShape+
		`<ParentFolderIds><t:DistinguishedFolderId Id="inbox" xmlns:t="`+nsTypes+`"/></ParentFolderIds></FindItem>`), true)
	checkExtended(t, "FindItem", found)

	itemID := itemIDRE.FindStringSubmatch(found)
	if len(itemID) != 2 {
		t.Fatalf("FindItem returned no ItemId: %s", found)
	}
	_, got := soapPost(t, ts, wrapRequest(`<GetItem xmlns="`+nsMessages+`">`+extShape+
		`<ItemIds><t:ItemId Id="`+itemID[1]+`" xmlns:t="`+nsTypes+`"/></ItemIds></GetItem>`), true)
	checkExtended(t, "GetItem", got)

	_, synced := soapPost(t, ts, wrapRequest(`<SyncFolderItems xmlns="`+nsMessages+`">`+extShape+
		`<SyncFolderId><t:DistinguishedFolderId Id="inbox" xmlns:t="`+nsTypes+`"/></SyncFolderId>`+
		`<MaxChangesReturned>10</MaxChangesReturned></SyncFolderItems>`), true)
	checkExtended(t, "SyncFolderItems", synced)
}

// TestItemShapeRefusesAnInvalidExtendedField proves a field URI that names no
// addressable property is refused rather than silently dropped.
func TestItemShapeRefusesAnInvalidExtendedField(t *testing.T) {
	ts, _ := seededWithMessage(t, plainMessage)
	_, out := soapPost(t, ts, wrapRequest(`<FindItem Traversal="Shallow" xmlns="`+nsMessages+`">`+
		`<ItemShape><BaseShape>Default</BaseShape><AdditionalProperties xmlns:t="`+nsTypes+`">`+
		`<t:ExtendedFieldURI PropertyTag="0x9000" PropertyType="String"/></AdditionalProperties></ItemShape>`+
		`<ParentFolderIds><t:DistinguishedFolderId Id="inbox" xmlns:t="`+nsTypes+`"/></ParentFolderIds></FindItem>`), true)
	if !strings.Contains(out, "ErrorInvalidExtendedProperty") {
		t.Errorf("an invalid field URI is not refused: %s", out)
	}
}

// getExtended reads one item with extShape.
func getExtended(t *testing.T, ts *httptest.Server, itemID string) string {
	t.Helper()
	_, out := soapPost(t, ts, wrapRequest(`<GetItem xmlns="`+nsMessages+`">`+extShape+
		`<ItemIds><t:ItemId Id="`+itemID+`" xmlns:t="`+nsTypes+`"/></ItemIds></GetItem>`), true)
	return out
}

// updateExtended sends one UpdateItem ItemChange with the given updates and
// returns the response.
func updateExtended(t *testing.T, ts *httptest.Server, itemID, updates string) string {
	t.Helper()
	_, out := soapPost(t, ts, wrapRequest(`<UpdateItem xmlns="`+nsMessages+`" xmlns:t="`+nsTypes+`"><ItemChanges><t:ItemChange>`+
		`<t:ItemId Id="`+itemID+`"/><t:Updates>`+updates+`</t:Updates></t:ItemChange></ItemChanges></UpdateItem>`), true)
	return out
}

// TestExtendedPropertiesAreWritten proves CreateItem stores the extended
// properties a message carries, UpdateItem sets one with SetItemField and removes
// one with DeleteItemField, and a value that does not fit its type is refused.
func TestExtendedPropertiesAreWritten(t *testing.T) {
	ts, _ := seededWithMessage(t)
	named := `<t:ExtendedFieldURI DistinguishedPropertySetId="PublicStrings" PropertyName="hx-mark" PropertyType="String"/>`
	tagged := `<t:ExtendedFieldURI PropertyTag="0x6801" PropertyType="Integer"/>`
	_, created := soapPost(t, ts, wrapRequest(`<CreateItem MessageDisposition="SaveOnly" xmlns="`+nsMessages+`" xmlns:t="`+nsTypes+`"><Items><t:Message>`+
		`<t:Subject>draft</t:Subject>`+
		`<t:ExtendedProperty>`+named+`<t:Value>marked</t:Value></t:ExtendedProperty>`+
		`<t:ExtendedProperty>`+tagged+`<t:Value>42</t:Value></t:ExtendedProperty>`+
		`</t:Message></Items></CreateItem>`), true)
	id := itemIDRE.FindStringSubmatch(created)
	if len(id) != 2 {
		t.Fatalf("CreateItem returned no ItemId: %s", created)
	}
	checkExtended(t, "GetItem after CreateItem", getExtended(t, ts, id[1]))

	updated := updateExtended(t, ts, id[1],
		`<t:SetItemField>`+named+`<t:Message><t:ExtendedProperty>`+named+`<t:Value>changed</t:Value></t:ExtendedProperty></t:Message></t:SetItemField>`+
			`<t:DeleteItemField>`+tagged+`</t:DeleteItemField>`)
	newID := itemIDRE.FindStringSubmatch(updated)
	if len(newID) != 2 {
		t.Fatalf("UpdateItem returned no ItemId: %s", updated)
	}
	got := getExtended(t, ts, newID[1])
	if !strings.Contains(got, "<Value>changed</Value>") || strings.Contains(got, "0x6801") {
		t.Errorf("after the update the item holds: %s", got)
	}

	bad := updateExtended(t, ts, newID[1],
		`<t:SetItemField>`+tagged+`<t:Message><t:ExtendedProperty>`+tagged+`<t:Value>many</t:Value></t:ExtendedProperty></t:Message></t:SetItemField>`)
	if !strings.Contains(bad, "ErrorInvalidExtendedPropertyValue") {
		t.Errorf("a value that is no integer is not refused: %s", bad)
	}
}
