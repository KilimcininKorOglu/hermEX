package ews

import (
	"encoding/xml"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

func findPeopleBody(query string) string {
	return `<FindPeople xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<QueryString>` + query + `</QueryString>` +
		`</FindPeople>`
}

func getPersonaBody(addr string) string {
	inner := ""
	if addr != "" {
		inner = `<t:EmailAddress>` + addr + `</t:EmailAddress>`
	}
	return `<GetPersona xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<EmailAddress>` + inner + `</EmailAddress>` +
		`</GetPersona>`
}

// parsedPersona mirrors the shape a client reads: the address lives in the inner
// element of the EmailAddress block, never on the block itself.
type parsedPersona struct {
	PersonaID struct {
		ID string `xml:"Id,attr"`
	} `xml:"PersonaId"`
	PersonaType  string `xml:"PersonaType"`
	DisplayName  string `xml:"DisplayName"`
	GivenName    string `xml:"GivenName"`
	Surname      string `xml:"Surname"`
	EmailAddress struct {
		Name        string `xml:"Name"`
		Address     string `xml:"EmailAddress"`
		RoutingType string `xml:"RoutingType"`
	} `xml:"EmailAddress"`
}

// parsedFindPeople reads the response the way a client does: ResponseClass on the
// FindPeopleResponse root, with no ResponseMessages wrapper, because FindPeople
// is not a batch operation.
type parsedFindPeople struct {
	Msg struct {
		Class    string          `xml:"ResponseClass,attr"`
		Code     string          `xml:"ResponseCode"`
		Personas []parsedPersona `xml:"People>Persona"`
		Total    *int            `xml:"TotalNumberOfPeopleInView"`
		FirstMat *int            `xml:"FirstMatchingRowIndex"`
		FirstLoa *int            `xml:"FirstLoadedRowIndex"`
	} `xml:"Body>FindPeopleResponse"`
}

type parsedGetPersona struct {
	Msg struct {
		Class   string        `xml:"ResponseClass,attr"`
		Code    string        `xml:"ResponseCode"`
		Persona parsedPersona `xml:"Persona"`
	} `xml:"Body>GetPersonaResponseMessage"`
}

// TestFindPeopleFromGAL proves FindPeople resolves a query against the GAL and
// returns one persona per match, carrying the directory display name and address.
func TestFindPeopleFromGAL(t *testing.T) {
	ts, _ := seededEWS(t)

	_, body := soapPost(t, ts, wrapRequest(findPeopleBody("alice")), true)
	var p parsedFindPeople
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse FindPeople: %v\n%s", err, body)
	}
	if p.Msg.Class != "Success" || p.Msg.Code != "NoError" {
		t.Fatalf("class/code = %q/%q, want Success/NoError\n%s", p.Msg.Class, p.Msg.Code, body)
	}
	if len(p.Msg.Personas) != 1 {
		t.Fatalf("got %d personas, want 1\n%s", len(p.Msg.Personas), body)
	}
	assertPersona(t, p.Msg.Personas[0], testUser)
	if p.Msg.Total == nil || *p.Msg.Total != 1 {
		t.Errorf("TotalNumberOfPeopleInView = %v, want 1", p.Msg.Total)
	}
}

// assertPersona checks the fields a client needs before it will show a row. It
// drops a persona that carries no type or no id, whatever else it holds, and it
// reads the address out of the inner element of the EmailAddress block.
func assertPersona(t *testing.T, got parsedPersona, address string) {
	t.Helper()
	if got.EmailAddress.Address != address {
		t.Errorf("persona address = %q, want %q", got.EmailAddress.Address, address)
	}
	if got.EmailAddress.RoutingType != "SMTP" {
		t.Errorf("routing type = %q, want SMTP", got.EmailAddress.RoutingType)
	}
	if got.DisplayName == "" {
		t.Error("persona carries no display name")
	}
	if got.PersonaType != "Person" {
		t.Errorf("persona type = %q, want Person", got.PersonaType)
	}
	if got.PersonaID.ID != address {
		t.Errorf("persona id = %q, want %q", got.PersonaID.ID, address)
	}
}

// TestFindPeopleIsNotABatchResponse pins the shape a client parses. Wrapping the
// answer in the ResponseMessages envelope the batch operations use makes the
// client discard every response whatever it holds, so autocomplete finds nobody
// even when the directory answers correctly.
func TestFindPeopleIsNotABatchResponse(t *testing.T) {
	ts, _ := seededEWS(t)

	_, body := soapPost(t, ts, wrapRequest(findPeopleBody("alice")), true)
	if strings.Contains(body, "FindPeopleResponseMessage") || strings.Contains(body, "ResponseMessages") {
		t.Fatalf("FindPeople answered in the batch envelope: %s", body)
	}
	if !strings.Contains(body, `<FindPeopleResponse xmlns="`+nsMessages+`" ResponseClass="Success">`) {
		t.Errorf("ResponseClass is not on the root element: %s", body)
	}
}

// TestFindPeopleSplitsANameItHas keeps the given name and surname off an address:
// a directory that holds only an address must not report that address as
// somebody's first name.
func TestFindPeopleSplitsANameItHas(t *testing.T) {
	for _, c := range []struct{ display, given, surname string }{
		{"Terry Adams", "Terry", "Adams"},
		{"Ada Lovelace Byron", "Ada", "Lovelace Byron"},
		{"alice@hermex.test", "", ""},
		{"", "", ""},
	} {
		given, surname := splitName(c.display)
		if given != c.given || surname != c.surname {
			t.Errorf("splitName(%q) = %q/%q, want %q/%q", c.display, given, surname, c.given, c.surname)
		}
	}
}

// TestFindPeopleNoMatch proves a query that matches nobody is a success with no
// people, not an error. The row counters are still emitted: a client reads them
// to decide whether to ask for another page, and an absent count is not the same
// answer as none.
func TestFindPeopleNoMatch(t *testing.T) {
	ts, _ := seededEWS(t)

	_, body := soapPost(t, ts, wrapRequest(findPeopleBody("nobodyhere")), true)
	var p parsedFindPeople
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse FindPeople: %v\n%s", err, body)
	}
	if p.Msg.Class != "Success" || p.Msg.Code != "NoError" {
		t.Fatalf("empty FindPeople not a success: %s", body)
	}
	if len(p.Msg.Personas) != 0 {
		t.Errorf("got %d personas, want 0", len(p.Msg.Personas))
	}
	if p.Msg.Total == nil || *p.Msg.Total != 0 {
		t.Errorf("TotalNumberOfPeopleInView = %v, want 0", p.Msg.Total)
	}
	if p.Msg.FirstMat == nil || p.Msg.FirstLoa == nil {
		t.Error("the row counters are missing from an empty answer")
	}
}

// TestFindPeopleSearchesTheUsersOwnContacts is why autocomplete is worth having:
// the address somebody corresponds with is often not in the organization's
// address book at all, and that is exactly the address the client is trying to
// complete.
func TestFindPeopleSearchesTheUsersOwnContacts(t *testing.T) {
	ts, dir := seededEWS(t)

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameEmail1Address})
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G115 -- a named-property id is a 16-bit value shifted into the tag's high half
	emailTag := mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtUnicode))
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDContacts), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Contact"},
		{Tag: mapi.PrDisplayName, Value: "Ada Lovelace"},
		{Tag: emailTag, Value: "ada@partner.example"},
	}}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	_, body := soapPost(t, ts, wrapRequest(findPeopleBody("Lovelace")), true)
	var p parsedFindPeople
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse FindPeople: %v\n%s", err, body)
	}
	if len(p.Msg.Personas) != 1 {
		t.Fatalf("got %d personas, want the contact\n%s", len(p.Msg.Personas), body)
	}
	got := p.Msg.Personas[0]
	if got.EmailAddress.Address != "ada@partner.example" {
		t.Errorf("address = %q, want the contact's", got.EmailAddress.Address)
	}
	if got.GivenName != "Ada" || got.Surname != "Lovelace" {
		t.Errorf("name split = %q/%q, want Ada/Lovelace", got.GivenName, got.Surname)
	}
}

// TestFindPeopleReportsOneAddressOnce keeps a person who is both a colleague and
// a saved contact from appearing twice in the autocomplete list.
func TestFindPeopleReportsOneAddressOnce(t *testing.T) {
	ts, dir := seededEWS(t)

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := st.GetNamedPropIDs(true, []mapi.PropertyName{mapi.NameEmail1Address})
	// #nosec G115 -- a named-property id is a 16-bit value shifted into the tag's high half
	emailTag := mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtUnicode))
	// The same address the GAL already answers with.
	if _, err := st.CreateMessage(int64(mapi.PrivateFIDContacts), &oxcmail.Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Contact"},
		{Tag: mapi.PrDisplayName, Value: "Alice Saved"},
		{Tag: emailTag, Value: testUser},
	}}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	_, body := soapPost(t, ts, wrapRequest(findPeopleBody("alice")), true)
	var p parsedFindPeople
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, persona := range p.Msg.Personas {
		if strings.EqualFold(persona.EmailAddress.Address, testUser) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the address appears %d times\n%s", n, body)
	}
}

// TestGetPersonaByAddress proves GetPersona returns the persona for a known GAL
// address.
func TestGetPersonaByAddress(t *testing.T) {
	ts, _ := seededEWS(t)

	_, body := soapPost(t, ts, wrapRequest(getPersonaBody(testUser)), true)
	var p parsedGetPersona
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse GetPersona: %v\n%s", err, body)
	}
	if p.Msg.Class != "Success" || p.Msg.Code != "NoError" {
		t.Fatalf("class/code = %q/%q, want Success/NoError\n%s", p.Msg.Class, p.Msg.Code, body)
	}
	if p.Msg.Persona.EmailAddress.Address != testUser {
		t.Errorf("persona address = %q, want %q", p.Msg.Persona.EmailAddress.Address, testUser)
	}
	if p.Msg.Persona.PersonaType != "Person" {
		t.Errorf("persona type = %q, want Person", p.Msg.Persona.PersonaType)
	}
}

// TestGetPersonaMissingAddress proves a request with no email address is
// ErrorInvalidArgument.
func TestGetPersonaMissingAddress(t *testing.T) {
	ts, _ := seededEWS(t)

	_, body := soapPost(t, ts, wrapRequest(getPersonaBody("")), true)
	if !strings.Contains(body, "ErrorInvalidArgument") {
		t.Fatalf("missing address: want ErrorInvalidArgument, got %s", body)
	}
}

// TestGetPersonaUnknownAddress proves an address absent from the GAL is
// ErrorPersonNotFound.
func TestGetPersonaUnknownAddress(t *testing.T) {
	ts, _ := seededEWS(t)

	_, body := soapPost(t, ts, wrapRequest(getPersonaBody("ghost@nowhere.test")), true)
	if !strings.Contains(body, "ErrorPersonNotFound") {
		t.Fatalf("unknown address: want ErrorPersonNotFound, got %s", body)
	}
}
