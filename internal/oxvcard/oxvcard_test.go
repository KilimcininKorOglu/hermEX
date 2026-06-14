package oxvcard

import (
	"fmt"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// resolver is a deterministic stand-in for the store's named-property allocator:
// stable ids >= 0x8000 keyed by the property name. The SAME instance must drive
// Import and Export so resolved proptags match across a round trip.
type resolver struct {
	ids  map[string]uint16
	next uint16
}

func newResolver() *resolver { return &resolver{ids: map[string]uint16{}, next: 0x8000} }

func nameKey(n mapi.PropertyName) string {
	return fmt.Sprintf("%v|%d|%s", n.GUID, n.LID, n.Name)
}

func (r *resolver) resolve(create bool, names []mapi.PropertyName) ([]uint16, error) {
	out := make([]uint16, len(names))
	for i, n := range names {
		k := nameKey(n)
		id, ok := r.ids[k]
		if !ok {
			if !create {
				continue
			}
			id = r.next
			r.next++
			r.ids[k] = id
		}
		out[i] = id
	}
	return out, nil
}

// namedVal reads a named string property's value from a message using the same
// resolver, so a test can assert which slot a vCard field landed in.
func namedVal(t *testing.T, r *resolver, m *oxcmail.Message, name mapi.PropertyName) string {
	t.Helper()
	ids, _ := r.resolve(false, []mapi.PropertyName{name})
	if ids[0] == 0 {
		return ""
	}
	tag := mapi.PropTag(uint32(ids[0])<<16 | uint32(mapi.PtUnicode))
	v, ok := m.Props.Get(tag)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func str(m *oxcmail.Message, tag mapi.PropTag) string {
	if v, ok := m.Props.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

const sampleVCard = "BEGIN:VCARD\r\n" +
	"VERSION:4.0\r\n" +
	"FN:Ada Lovelace\r\n" +
	"N:Lovelace;Ada;Augusta;Ms.;\r\n" +
	"NICKNAME:Countess\r\n" +
	"TITLE:Mathematician\r\n" +
	"ORG:Analytical Engine;Research\r\n" +
	"EMAIL;TYPE=work:ada@work.test\r\n" +
	"EMAIL;TYPE=home:ada@home.test\r\n" +
	"TEL;TYPE=cell:+1-555-0100\r\n" +
	"TEL;TYPE=work:+1-555-0199\r\n" +
	"ADR;TYPE=home:;;1 Mill St;London;;EC1;UK\r\n" +
	"ADR;TYPE=work:;;2 Engine Rd;London;;EC2;UK\r\n" +
	"NOTE:First programmer.\r\n" +
	"CATEGORIES:science,history\r\n" +
	"UID:ada-0001\r\n" +
	"END:VCARD\r\n"

// TestImportMapping verifies that each vCard field lands on the correct MAPI
// property (intent: the mobile number must be the mobile tag, the second email
// the second slot, the work street a work-address named prop).
func TestImportMapping(t *testing.T) {
	r := newResolver()
	m, err := Import([]byte(sampleVCard), Options{Resolver: r.resolve})
	if err != nil {
		t.Fatal(err)
	}
	if got := str(m, mapi.PrMessageClass); got != "IPM.Contact" {
		t.Errorf("message class %q, want IPM.Contact", got)
	}
	if got := str(m, mapi.PrDisplayName); got != "Ada Lovelace" {
		t.Errorf("display name %q", got)
	}
	if got := str(m, mapi.PrSurname); got != "Lovelace" {
		t.Errorf("surname %q", got)
	}
	if got := str(m, mapi.PrGivenName); got != "Ada" {
		t.Errorf("given name %q", got)
	}
	if got := str(m, mapi.PrCompanyName); got != "Analytical Engine" {
		t.Errorf("company %q", got)
	}
	if got := str(m, mapi.PrDepartmentName); got != "Research" {
		t.Errorf("department %q", got)
	}
	if got := str(m, mapi.PrMobileTelephoneNumber); got != "+1-555-0100" {
		t.Errorf("mobile %q", got)
	}
	if got := str(m, mapi.PrBusinessTelephoneNumber); got != "+1-555-0199" {
		t.Errorf("business phone %q", got)
	}
	if got := str(m, mapi.PrHomeAddressCity); got != "London" {
		t.Errorf("home city %q", got)
	}
	if got := namedVal(t, r, m, mapi.NameEmail1Address); got != "ada@work.test" {
		t.Errorf("email1 %q", got)
	}
	if got := namedVal(t, r, m, mapi.NameEmail2Address); got != "ada@home.test" {
		t.Errorf("email2 %q", got)
	}
	if got := namedVal(t, r, m, mapi.NameWorkAddressStreet); got != "2 Engine Rd" {
		t.Errorf("work street %q", got)
	}
	if got := namedVal(t, r, m, nameVCardUID); got != "ada-0001" {
		t.Errorf("uid %q", got)
	}
}

// TestRoundTrip imports, exports, and re-imports, asserting the contact survives
// a full vCard cycle through the property bag.
func TestRoundTrip(t *testing.T) {
	r := newResolver()
	opt := Options{Resolver: r.resolve}
	m1, err := Import([]byte(sampleVCard), opt)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Export(m1, opt)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Import(out, opt)
	if err != nil {
		t.Fatalf("re-import: %v\n%s", err, out)
	}
	for _, tag := range []mapi.PropTag{
		mapi.PrDisplayName, mapi.PrSurname, mapi.PrGivenName, mapi.PrMiddleName,
		mapi.PrCompanyName, mapi.PrDepartmentName, mapi.PrMobileTelephoneNumber,
		mapi.PrBusinessTelephoneNumber, mapi.PrHomeAddressCity,
	} {
		if str(m1, tag) != str(m2, tag) {
			t.Errorf("tag %#x: %q != %q after round trip", uint32(tag), str(m1, tag), str(m2, tag))
		}
	}
	for _, n := range []mapi.PropertyName{mapi.NameEmail1Address, mapi.NameEmail2Address, mapi.NameWorkAddressStreet, nameVCardUID} {
		if namedVal(t, r, m1, n) != namedVal(t, r, m2, n) {
			t.Errorf("named %v: %q != %q after round trip", n, namedVal(t, r, m1, n), namedVal(t, r, m2, n))
		}
	}
}

// TestRejectVersion21 confirms a vCard 2.1 card is rejected, as the converter
// only supports 3.0 and 4.0.
func TestRejectVersion21(t *testing.T) {
	r := newResolver()
	card := "BEGIN:VCARD\r\nVERSION:2.1\r\nFN:Old Style\r\nEND:VCARD\r\n"
	if _, err := Import([]byte(card), Options{Resolver: r.resolve}); err != errVersion {
		t.Errorf("err %v, want errVersion", err)
	}
}

// TestExportShape checks the emitted vCard opens and closes correctly and is
// version 4.0.
func TestExportShape(t *testing.T) {
	r := newResolver()
	opt := Options{Resolver: r.resolve}
	m, _ := Import([]byte(sampleVCard), opt)
	out, err := Export(m, opt)
	if err != nil {
		t.Fatal(err)
	}
	card, err := parseVCard(out)
	if err != nil {
		t.Fatalf("export not parseable: %v", err)
	}
	if card.version() != "4.0" {
		t.Errorf("exported version %q, want 4.0", card.version())
	}
	if card.get("FN") == nil || card.get("FN").text() != "Ada Lovelace" {
		t.Error("exported FN missing/wrong")
	}
}
