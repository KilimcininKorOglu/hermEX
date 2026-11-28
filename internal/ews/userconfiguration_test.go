package ews

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// --- parse helpers for the GetUserConfiguration response ---

type ucObjParse struct {
	Type   string   `xml:"Type"`
	Values []string `xml:"Value"`
}

type ucEntryParse struct {
	Key   ucObjParse `xml:"DictionaryKey"`
	Value ucObjParse `xml:"DictionaryValue"`
}

type ucConfigParse struct {
	ItemID struct {
		ID string `xml:"Id,attr"`
	} `xml:"ItemId"`
	Entries []ucEntryParse `xml:"Dictionary>DictionaryEntry"`
	XMLData string         `xml:"XmlData"`
	BinData string         `xml:"BinaryData"`
}

type ucMsgParse struct {
	Class  string        `xml:"ResponseClass,attr"`
	Code   string        `xml:"ResponseCode"`
	Config ucConfigParse `xml:"UserConfiguration"`
}

type parsedGetUC struct {
	Msg ucMsgParse `xml:"Body>GetUserConfigurationResponse>ResponseMessages>GetUserConfigurationResponseMessage"`
}

// dictEntryXML builds a single typed DictionaryEntry. The value is given as a
// slice so an array-typed entry (multiple Value elements) is expressible.
func dictEntryXML(keyType, key, valType string, vals ...string) string {
	parts := make([]string, 0, len(vals)+4)
	parts = append(parts,
		`<t:DictionaryEntry><t:DictionaryKey><t:Type>`+keyType+`</t:Type><t:Value>`+key+`</t:Value></t:DictionaryKey>`,
		`<t:DictionaryValue><t:Type>`+valType+`</t:Type>`,
	)
	for _, v := range vals {
		parts = append(parts, `<t:Value>`+v+`</t:Value>`)
	}
	parts = append(parts, `</t:DictionaryValue></t:DictionaryEntry>`)
	return strings.Join(parts, "")
}

// createUCBody builds a CreateUserConfiguration request for the named config on a
// distinguished folder, with the given dictionary entries and XML/binary blobs.
func createUCBody(op, name, folder, dict, xmlData, binData string) string {
	return `<` + op + ` xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<UserConfiguration>` +
		`<t:UserConfigurationName Name="` + name + `"><t:DistinguishedFolderId Id="` + folder + `"/></t:UserConfigurationName>` +
		`<t:Dictionary>` + dict + `</t:Dictionary>` +
		`<t:XmlData>` + xmlData + `</t:XmlData>` +
		`<t:BinaryData>` + binData + `</t:BinaryData>` +
		`</UserConfiguration></` + op + `>`
}

func getUCBody(name, folder, props string) string {
	return `<GetUserConfiguration xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<UserConfigurationName Name="` + name + `"><t:DistinguishedFolderId Id="` + folder + `"/></UserConfigurationName>` +
		`<UserConfigurationProperties>` + props + `</UserConfigurationProperties>` +
		`</GetUserConfiguration>`
}

func deleteUCBody(name, folder string) string {
	return `<DeleteUserConfiguration xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<UserConfigurationName Name="` + name + `"><t:DistinguishedFolderId Id="` + folder + `"/></UserConfigurationName>` +
		`</DeleteUserConfiguration>`
}

func mustParseGetUC(t *testing.T, body string) parsedGetUC {
	t.Helper()
	var p parsedGetUC
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse GetUserConfiguration: %v\n%s", err, body)
	}
	return p
}

// mixedDict is a dictionary that exercises every dictionary-object type the
// round-trip must preserve, including an array-typed (multi-value) entry.
func mixedDict() string {
	return dictEntryXML("String", "Phone", "String", "555-1111") +
		dictEntryXML("String", "Age", "Integer32", "42") +
		dictEntryXML("String", "Active", "Boolean", "true") +
		dictEntryXML("String", "Created", "DateTime", "2020-01-02T03:04:05Z") +
		dictEntryXML("String", "Blob", "ByteArray", "AQIDBA==") +
		dictEntryXML("String", "Tags", "StringArray", "alpha", "beta", "gamma")
}

// TestUserConfigCreateGetRoundTripTypes is the load-bearing test: a config created
// with a mix of dictionary-object types (and an array-typed entry) must come back
// from GetUserConfiguration with every type AND value preserved, and the XML and
// binary blobs byte-identical. A type-flattening or single-value bug fails here
// while a string-only round-trip would pass.
func TestUserConfigCreateGetRoundTripTypes(t *testing.T) {
	ts, _ := seededEWS(t)

	const xmlBlob = "PHJvb3QvPg==" // <root/>
	const binBlob = "AAECAwQF"     // 0,1,2,3,4,5
	create := createUCBody("CreateUserConfiguration", "RoamCfg", "drafts", mixedDict(), xmlBlob, binBlob)
	unauthResp, _ := soapPost(t, ts, wrapRequest(create), false)
	wantEq(t, "unauth CreateUserConfiguration status", unauthResp.StatusCode, http.StatusUnauthorized)

	cHTTP, cresp := soapPost(t, ts, wrapRequest(create), true)
	wantEq(t, "CreateUserConfiguration status", cHTTP.StatusCode, http.StatusOK)
	if !strings.Contains(cresp, `ResponseClass="Success"`) {
		t.Fatalf("CreateUserConfiguration failed: %s", cresp)
	}

	unauthGetResp, unauthGetBody := soapPost(t, ts, wrapRequest(getUCBody("RoamCfg", "drafts", "All")), false)
	wantEq(t, "unauth GetUserConfiguration status", unauthGetResp.StatusCode, http.StatusUnauthorized)
	wantNotContains(t, "unauth GetUserConfiguration body", unauthGetBody, xmlBlob)

	gHTTP, gresp := soapPost(t, ts, wrapRequest(getUCBody("RoamCfg", "drafts", "All")), true)
	wantEq(t, "GetUserConfiguration status", gHTTP.StatusCode, http.StatusOK)
	p := mustParseGetUC(t, gresp)
	if p.Msg.Code != "NoError" {
		t.Fatalf("GetUserConfiguration code = %q, want NoError\n%s", p.Msg.Code, gresp)
	}
	if p.Msg.Config.ItemID.ID == "" {
		t.Error("response carries no ItemId")
	}
	wantEq(t, "the XmlData (verbatim)", p.Msg.Config.XMLData, xmlBlob)
	wantEq(t, "the BinaryData (verbatim)", p.Msg.Config.BinData, binBlob)

	want := []struct {
		key, valType string
		vals         []string
	}{
		{"Phone", "String", []string{"555-1111"}},
		{"Age", "Integer32", []string{"42"}},
		{"Active", "Boolean", []string{"true"}},
		{"Created", "DateTime", []string{"2020-01-02T03:04:05Z"}},
		{"Blob", "ByteArray", []string{"AQIDBA=="}},
		{"Tags", "StringArray", []string{"alpha", "beta", "gamma"}},
	}
	if len(p.Msg.Config.Entries) != len(want) {
		t.Fatalf("got %d dictionary entries, want %d\n%s", len(p.Msg.Config.Entries), len(want), gresp)
	}
	for i, w := range want {
		e := p.Msg.Config.Entries[i]
		label := fmt.Sprintf("entry %d (%s)", i, w.key)
		wantEq(t, label+" key type", e.Key.Type, "String")
		wantEq(t, label+" key", strings.Join(e.Key.Values, ","), w.key)
		wantEq(t, label+" value type", e.Value.Type, w.valType)
		wantEq(t, label+" values", strings.Join(e.Value.Values, ","), strings.Join(w.vals, ","))
	}
}

// TestUserConfigGetMissing proves an absent config is ErrorItemNotFound, not a
// success with an empty body.
func TestUserConfigGetMissing(t *testing.T) {
	ts, _ := seededEWS(t)
	resp, gresp := soapPost(t, ts, wrapRequest(getUCBody("Nope", "drafts", "All")), true)
	wantEq(t, "missing GetUserConfiguration status", resp.StatusCode, http.StatusOK)
	p := mustParseGetUC(t, gresp)
	if p.Msg.Code != "ErrorItemNotFound" {
		t.Fatalf("missing config code = %q, want ErrorItemNotFound\n%s", p.Msg.Code, gresp)
	}
}

// TestUserConfigUpdate proves UpdateUserConfiguration replaces a config's content,
// and that updating a config that does not exist is ErrorItemNotFound.
func TestUserConfigUpdate(t *testing.T) {
	ts, _ := seededEWS(t)

	create := createUCBody("CreateUserConfiguration", "Cfg", "drafts",
		dictEntryXML("String", "Color", "String", "red"), "", "")
	if resp, r := soapPost(t, ts, wrapRequest(create), true); resp.StatusCode != http.StatusOK || !strings.Contains(r, `ResponseClass="Success"`) {
		t.Fatalf("create failed (%d): %s", resp.StatusCode, r)
	}

	update := createUCBody("UpdateUserConfiguration", "Cfg", "drafts",
		dictEntryXML("String", "Color", "String", "green"), "", "")
	if resp, r := soapPost(t, ts, wrapRequest(update), true); resp.StatusCode != http.StatusOK || !strings.Contains(r, `ResponseClass="Success"`) {
		t.Fatalf("update failed (%d): %s", resp.StatusCode, r)
	}

	gResp, gresp := soapPost(t, ts, wrapRequest(getUCBody("Cfg", "drafts", "Dictionary")), true)
	wantEq(t, "updated GetUserConfiguration status", gResp.StatusCode, http.StatusOK)
	p := mustParseGetUC(t, gresp)
	if len(p.Msg.Config.Entries) != 1 || len(p.Msg.Config.Entries[0].Value.Values) != 1 ||
		p.Msg.Config.Entries[0].Value.Values[0] != "green" {
		t.Fatalf("after update value = %+v, want green\n%s", p.Msg.Config.Entries, gresp)
	}

	missing := createUCBody("UpdateUserConfiguration", "Ghost", "drafts",
		dictEntryXML("String", "x", "String", "y"), "", "")
	mHTTP, mresp := soapPost(t, ts, wrapRequest(missing), true)
	wantEq(t, "missing UpdateUserConfiguration status", mHTTP.StatusCode, http.StatusOK)
	if !strings.Contains(mresp, "ErrorItemNotFound") {
		t.Errorf("update of missing config: want ErrorItemNotFound, got %s", mresp)
	}
}

// TestUserConfigDelete proves DeleteUserConfiguration removes a config (a later
// Get is ErrorItemNotFound) and that deleting a missing config is ErrorItemNotFound.
func TestUserConfigDelete(t *testing.T) {
	ts, _ := seededEWS(t)

	create := createUCBody("CreateUserConfiguration", "Cfg", "drafts",
		dictEntryXML("String", "k", "String", "v"), "", "")
	if resp, r := soapPost(t, ts, wrapRequest(create), true); resp.StatusCode != http.StatusOK || !strings.Contains(r, `ResponseClass="Success"`) {
		t.Fatalf("create failed (%d): %s", resp.StatusCode, r)
	}

	if resp, r := soapPost(t, ts, wrapRequest(deleteUCBody("Cfg", "drafts")), true); resp.StatusCode != http.StatusOK || !strings.Contains(r, `ResponseClass="Success"`) {
		t.Fatalf("delete failed (%d): %s", resp.StatusCode, r)
	}
	gHTTP, gresp := soapPost(t, ts, wrapRequest(getUCBody("Cfg", "drafts", "All")), true)
	wantEq(t, "deleted GetUserConfiguration status", gHTTP.StatusCode, http.StatusOK)
	if p := mustParseGetUC(t, gresp); p.Msg.Code != "ErrorItemNotFound" {
		t.Errorf("after delete, Get code = %q, want ErrorItemNotFound", p.Msg.Code)
	}

	dHTTP, dresp := soapPost(t, ts, wrapRequest(deleteUCBody("Ghost", "drafts")), true)
	wantEq(t, "missing DeleteUserConfiguration status", dHTTP.StatusCode, http.StatusOK)
	if !strings.Contains(dresp, "ErrorItemNotFound") {
		t.Errorf("delete of missing config: want ErrorItemNotFound, got %s", dresp)
	}
}

// TestUserConfigForeignMailboxDenied proves the own-mailbox gate: a config request
// whose folder names another mailbox is refused with ErrorAccessDenied, never
// reaching another mailbox's store.
func TestUserConfigForeignMailboxDenied(t *testing.T) {
	ts, _ := seededEWS(t)
	body := `<GetUserConfiguration xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<UserConfigurationName Name="Cfg">` +
		`<t:DistinguishedFolderId Id="drafts"><t:Mailbox><t:EmailAddress>victim@hermex.test</t:EmailAddress></t:Mailbox></t:DistinguishedFolderId>` +
		`</UserConfigurationName>` +
		`<UserConfigurationProperties>All</UserConfigurationProperties>` +
		`</GetUserConfiguration>`
	resp, gresp := soapPost(t, ts, wrapRequest(body), true)
	wantEq(t, "foreign GetUserConfiguration status", resp.StatusCode, http.StatusOK)
	if !strings.Contains(gresp, "ErrorAccessDenied") {
		t.Fatalf("foreign-mailbox config: want ErrorAccessDenied, got %s", gresp)
	}
}
