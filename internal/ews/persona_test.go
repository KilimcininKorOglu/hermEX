package ews

import (
	"encoding/xml"
	"strings"
	"testing"
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

type parsedFindPeople struct {
	Msg struct {
		Class    string `xml:"ResponseClass,attr"`
		Code     string `xml:"ResponseCode"`
		Personas []struct {
			DisplayName  string `xml:"DisplayName"`
			EmailAddress string `xml:"EmailAddress"`
		} `xml:"People>Persona"`
		Total *int `xml:"TotalNumberOfPeopleInView"`
	} `xml:"Body>FindPeopleResponse>ResponseMessages>FindPeopleResponseMessage"`
}

type parsedGetPersona struct {
	Msg struct {
		Class   string `xml:"ResponseClass,attr"`
		Code    string `xml:"ResponseCode"`
		Persona struct {
			DisplayName  string `xml:"DisplayName"`
			EmailAddress string `xml:"EmailAddress"`
		} `xml:"Persona"`
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
	if p.Msg.Personas[0].EmailAddress != testUser {
		t.Errorf("persona address = %q, want %q", p.Msg.Personas[0].EmailAddress, testUser)
	}
	if p.Msg.Personas[0].DisplayName == "" {
		t.Error("persona carries no display name")
	}
	if p.Msg.Total == nil || *p.Msg.Total != 1 {
		t.Errorf("TotalNumberOfPeopleInView = %v, want 1", p.Msg.Total)
	}
}

// TestFindPeopleNoMatch proves a query that matches nobody is a success with no
// people and no total, not an error.
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
	if p.Msg.Persona.EmailAddress != testUser {
		t.Errorf("persona address = %q, want %q", p.Msg.Persona.EmailAddress, testUser)
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
