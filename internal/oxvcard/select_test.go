package oxvcard

import (
	"strings"
	"testing"
)

const selectCardFull = "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Jane Doe\r\nN:Doe;Jane;;;\r\n" +
	"EMAIL;TYPE=work:jane@example.com\r\nTEL:+1-555-1234\r\nNOTE:private\r\nEND:VCARD\r\n"

// TestSelectAddressData keeps only the named properties, dropping the rest while
// preserving the BEGIN/END structure (RFC 6352 §10.4).
func TestSelectAddressData(t *testing.T) {
	out, ok := SelectAddressData([]byte(selectCardFull),
		[]PropSelect{{Name: "VERSION"}, {Name: "FN"}, {Name: "EMAIL"}}, false)
	if !ok {
		t.Fatal("SelectAddressData returned ok=false")
	}
	s := string(out)
	if !strings.Contains(s, "VERSION:4.0") || !strings.Contains(s, "FN:Jane Doe") || !strings.Contains(s, "EMAIL;TYPE=work:jane@example.com") {
		t.Errorf("a selected property is missing\n%s", s)
	}
	for _, leak := range []string{"TEL:", "NOTE:", "\r\nN:"} {
		if strings.Contains(s, leak) {
			t.Errorf("unselected %q leaked into the projection\n%s", leak, s)
		}
	}
}

// TestSelectAddressDataNoValue confirms novalue keeps the name and parameters but drops
// the value.
func TestSelectAddressDataNoValue(t *testing.T) {
	out, _ := SelectAddressData([]byte(selectCardFull), []PropSelect{{Name: "EMAIL", NoValue: true}}, false)
	s := string(out)
	if !strings.Contains(s, "EMAIL;TYPE=work:\r\n") {
		t.Errorf("novalue should keep the name and parameters with a bare colon\n%s", s)
	}
	if strings.Contains(s, "jane@example.com") {
		t.Errorf("novalue must drop the value\n%s", s)
	}
}

// TestSelectAddressDataAllProp confirms allprop keeps every property.
func TestSelectAddressDataAllProp(t *testing.T) {
	out, _ := SelectAddressData([]byte(selectCardFull), nil, true)
	s := string(out)
	if !strings.Contains(s, "TEL:+1-555-1234") || !strings.Contains(s, "NOTE:private") {
		t.Errorf("allprop should keep every property\n%s", s)
	}
}
