package webmail2api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestBuildVCardFullContact pins the exact vCard a fully populated contact
// renders to. The bytes are the interface to oxvcard.Import, which decides
// which MAPI property each line lands on, so the property order, the parameter
// spelling (TYPE=CELL against TYPE=fax,work) and the semicolon layout of the
// structured N/ORG/ADR values are all load-bearing rather than cosmetic.
func TestBuildVCardFullContact(t *testing.T) {
	c := contactJSON{
		Name: "Ayşe Yılmaz", FirstName: "Ayşe", LastName: "Yılmaz", MiddleName: "Nur",
		Prefix: "Dr.", Suffix: "PhD", Nickname: "Ay",
		Email: "ayse@example.test", Email2: "a.yilmaz@example.test", Email3: "ay@example.test",
		Phone: "+90 212 000 0000", MobilePhone: "+90 532 000 0000",
		HomePhone: "+90 216 000 0000", BusinessFax: "+90 212 000 0001",
		Company: "Örnek A.Ş.", Department: "Ar-Ge", JobTitle: "Mühendis", Profession: "Yazılım",
		Birthday:   "1990-04-15",
		HomeStreet: "Ev Sokak 1", HomeCity: "İstanbul", HomeState: "Marmara", HomePostal: "34000", HomeCountry: "Türkiye",
		WorkStreet: "İş Caddesi 2", WorkCity: "Ankara", WorkState: "İç Anadolu", WorkPostal: "06000", WorkCountry: "Türkiye",
		OtherStreet: "Diğer Yol 3", OtherCity: "İzmir", OtherState: "Ege", OtherPostal: "35000", OtherCountry: "Türkiye",
		IMAddress: "ayse@im.example.test", WebPage: "https://example.test/ayse",
	}

	want := "BEGIN:VCARD\r\n" +
		"VERSION:4.0\r\n" +
		"FN:Ayşe Yılmaz\r\n" +
		"N:Yılmaz;Ayşe;Nur;Dr.;PhD\r\n" +
		"EMAIL:ayse@example.test\r\n" +
		"EMAIL:a.yilmaz@example.test\r\n" +
		"EMAIL:ay@example.test\r\n" +
		"NICKNAME:Ay\r\n" +
		"TEL;TYPE=work:+90 212 000 0000\r\n" +
		"TEL;TYPE=CELL:+90 532 000 0000\r\n" +
		"TEL;TYPE=HOME:+90 216 000 0000\r\n" +
		"TEL;TYPE=fax,work:+90 212 000 0001\r\n" +
		"ORG:Örnek A.Ş.;Ar-Ge\r\n" +
		"TITLE:Mühendis\r\n" +
		"ROLE:Yazılım\r\n" +
		"BDAY:1990-04-15\r\n" +
		"ADR;TYPE=HOME:;;Ev Sokak 1;İstanbul;Marmara;34000;Türkiye\r\n" +
		"ADR;TYPE=WORK:;;İş Caddesi 2;Ankara;İç Anadolu;06000;Türkiye\r\n" +
		"ADR;TYPE=OTHER:;;Diğer Yol 3;İzmir;Ege;35000;Türkiye\r\n" +
		"IMPP:ayse@im.example.test\r\n" +
		"URL:https://example.test/ayse\r\n" +
		"END:VCARD\r\n"

	if got := string(buildVCard(c)); got != want {
		t.Errorf("vCard mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestBuildVCardOmitsEmptyFields proves an empty field emits no line at all: a
// bare name renders the envelope plus FN only, so a contact created with one
// field does not carry empty TEL/ADR/ORG lines that oxvcard would import as
// blank MAPI properties.
func TestBuildVCardOmitsEmptyFields(t *testing.T) {
	want := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Yalnız İsim\r\nEND:VCARD\r\n"
	if got := string(buildVCard(contactJSON{Name: "Yalnız İsim"})); got != want {
		t.Errorf("vCard mismatch\ngot:\n%q\nwant:\n%q", got, want)
	}
}

// TestBuildVCardEscapesValues: RFC 6350 section 3.4 escapes a backslash,
// comma, semicolon and line break inside a value, so a field cannot add a line
// of its own or shift the components of a structured value.
func TestBuildVCardEscapesValues(t *testing.T) {
	got := string(buildVCard(contactJSON{
		Name:    "Eve\r\nEMAIL:attacker@evil.test",
		Company: "A;B",
		Email:   "eve@example.test",
	}))
	for _, line := range strings.Split(got, "\r\n") {
		if line == "EMAIL:attacker@evil.test" {
			t.Fatalf("a field injected its own vCard line:\n%s", got)
		}
	}
	wantContains(t, "escaped FN", got, `FN:Eve\nEMAIL:attacker@evil.test`)
	wantContains(t, "escaped ORG", got, `ORG:A\;B`)
}

// TestEscapedContactFieldsRoundTrip: values carrying the vCard special
// characters come back exactly as saved, so the escaping is undone on import.
func TestEscapedContactFieldsRoundTrip(t *testing.T) {
	do := apiDo(t)
	wantStatus(t, "create", do(http.MethodPost, "/api/v1/contacts",
		`{"name":"Smith, John","company":"A;B","department":"R\\D","email":"j@example.test"}`), http.StatusOK)
	var list struct {
		Contacts []contactJSON `json:"contacts"`
	}
	rec := do(http.MethodGet, "/api/v1/contacts", "")
	wantStatus(t, "list", rec, http.StatusOK)
	mustNoErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &list))
	if len(list.Contacts) != 1 {
		t.Fatalf("got %d contacts, want 1", len(list.Contacts))
	}
	c := list.Contacts[0]
	wantEq(t, "name", c.Name, "Smith, John")
	wantEq(t, "company", c.Company, "A;B")
	wantEq(t, "department", c.Department, `R\D`)
}

// TestBuildVCardStructuredValuesAreGrouped proves the structured properties are
// emitted whenever ANY of their components is set, with the unset components
// left empty in place: N from one name part alone, ORG from a department with
// no company, and ADR from a city alone. Dropping a group because its first
// component is empty would move every later component into the wrong vCard
// field on import.
func TestBuildVCardStructuredValuesAreGrouped(t *testing.T) {
	cases := []struct {
		name string
		in   contactJSON
		want string
	}{
		{"middle name alone", contactJSON{MiddleName: "Nur"}, "N:;;Nur;;\r\n"},
		{"suffix alone", contactJSON{Suffix: "PhD"}, "N:;;;;PhD\r\n"},
		{"department with no company", contactJSON{Department: "Ar-Ge"}, "ORG:;Ar-Ge\r\n"},
		{"home city alone", contactJSON{HomeCity: "İzmir"}, "ADR;TYPE=HOME:;;;İzmir;;;\r\n"},
		{"work country alone", contactJSON{WorkCountry: "Türkiye"}, "ADR;TYPE=WORK:;;;;;;Türkiye\r\n"},
		{"other postal alone", contactJSON{OtherPostal: "35000"}, "ADR;TYPE=OTHER:;;;;;35000;\r\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(buildVCard(c.in))
			if !strings.Contains(got, c.want) {
				t.Errorf("vCard does not carry %q:\n%s", c.want, got)
			}
		})
	}
}
