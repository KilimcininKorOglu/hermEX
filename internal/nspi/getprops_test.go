package nspi

import (
	"testing"

	"hermex/internal/directory"
	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// profileGAL is a GAL source that also serves user properties, exercising the lazy
// profile-field path in GetProps (StaticAccounts alone has no GetUserProperties).
type profileGAL struct {
	directory.StaticAccounts
	props map[string]map[uint32]string
}

func (p profileGAL) GetUserProperties(username string) (map[uint32]string, error) {
	return p.props[username], nil
}

// TestGetPropsProfileFields proves the GAL serves a user's directory profile fields
// (title, department) so they appear in Outlook's detail view.
func TestGetPropsProfileFields(t *testing.T) {
	gal := profileGAL{
		StaticAccounts: directory.StaticAccounts{"alice@hermex.test": {Password: "x", MailboxPath: "/mb/a"}},
		props: map[string]map[uint32]string{
			"alice@hermex.test": {uint32(mapi.PrTitle): "Engineer", uint32(mapi.PrDepartmentName): "R&D"},
		},
	}
	s := NewServer(gal, testGUID)
	_, row := decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midBase, codePage: 1252},
		[]mapi.PropTag{mapi.PrTitle, mapi.PrDepartmentName})))
	if v, _ := row.Get(mapi.PrTitle); v != "Engineer" {
		t.Errorf("PrTitle = %v, want Engineer", v)
	}
	if v, _ := row.Get(mapi.PrDepartmentName); v != "R&D" {
		t.Errorf("PrDepartmentName = %v, want R&D", v)
	}
}

// buildGetProps frames a GetProps request: flags + a STAT (carrying cur_rec and
// the code page) + an optional explicit column set + an empty auxiliary buffer.
func buildGetProps(st stat, cols []mapi.PropTag) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0) // flags
	p.Uint8(1)  // hasStat
	pushStat(p, st)
	if cols == nil {
		p.Uint8(0)
	} else {
		p.Uint8(1)
		_ = p.PropTagsLong(cols)
	}
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// decodeGetProps reads a GetProps response up to the row, returning the result
// code and the decoded property bag (nil when the response carries no row).
func decodeGetProps(t *testing.T, resp []byte) (uint32, mapi.PropertyValues) {
	t.Helper()
	p := ext.NewPull(resp, abkFlags)
	if status := mustU32(t, p, "status"); status != 0 {
		t.Fatalf("status = %#x, want 0", status)
	}
	result := mustU32(t, p, "result")
	mustU32(t, p, "codepage")
	marker := mustU8(t, p, "row marker")
	if marker == 0 {
		return result, nil
	}
	row, err := p.PropertyValuesLong()
	if err != nil {
		t.Fatalf("decode row: %v", err)
	}
	if aux := mustU32(t, p, "auxout"); aux != 0 {
		t.Errorf("auxout = %d, want 0", aux)
	}
	return result, row
}

// TestGetPropsDefault proves a no-column GetProps on the first entry (cur_rec ==
// midBase) returns that entry's full default property bag.
func TestGetPropsDefault(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test")
	result, row := decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midBase, codePage: 1252}, nil)))
	if result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	if len(row) != len(defaultColumns) {
		t.Errorf("row has %d props, want %d (default bag)", len(row), len(defaultColumns))
	}
	if v, _ := row.Get(mapi.PrSmtpAddress); v != "alice@hermex.test" {
		t.Errorf("SMTP = %v, want alice (cur_rec=midBase resolves to the first entry)", v)
	}
}

// TestGetPropsByMID proves cur_rec at a non-first entry MId routes to a direct
// lookup (not a positional resolution): midBase+1 is the second address-sorted
// entry, bob.
func TestGetPropsByMID(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	_, row := decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midBase + 1, codePage: 1252}, nil)))
	if v, _ := row.Get(mapi.PrSmtpAddress); v != "bob@hermex.test" {
		t.Errorf("cur_rec=midBase+1 SMTP = %v, want bob", v)
	}
}

// TestGetPropsExplicitError proves explicit columns project present values and
// mark an absent column as a PT_ERROR(ecNotFound), with ecWarnWithErrors.
func TestGetPropsExplicitError(t *testing.T) {
	s := testGAL("alice@hermex.test")
	cols := []mapi.PropTag{mapi.PrDisplayName, mapi.PrContainerFlags} // the 2nd is not a GAL-user prop
	result, row := decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midBase, codePage: 1252}, cols)))
	if result != ecWarnWithErrors {
		t.Fatalf("result = %#x, want ecWarnWithErrors", result)
	}
	if _, ok := row.Get(mapi.PrDisplayName); !ok {
		t.Error("present column PrDisplayName missing from row")
	}
	errTag := errorTag(mapi.PrContainerFlags)
	v, ok := row.Get(errTag)
	if !ok {
		t.Fatalf("absent column not marked PT_ERROR (tag %#x)", uint32(errTag))
	}
	if v != ecNotFound {
		t.Errorf("PT_ERROR value = %v, want ecNotFound", v)
	}
}

// TestGetPropsUnknownEntry proves an unresolvable cur_rec (END_OF_TABLE) yields a
// row of PT_ERROR markers over the default columns, with ecWarnWithErrors.
func TestGetPropsUnknownEntry(t *testing.T) {
	s := testGAL("alice@hermex.test")
	result, row := decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midEndOfTable, codePage: 1252}, nil)))
	if result != ecWarnWithErrors {
		t.Fatalf("result = %#x, want ecWarnWithErrors", result)
	}
	if len(row) != len(defaultColumns) {
		t.Fatalf("error row has %d props, want %d", len(row), len(defaultColumns))
	}
	if v, _ := row.Get(errorTag(mapi.PrSmtpAddress)); v != ecNotFound {
		t.Errorf("error row PrSmtpAddress marker = %v, want ecNotFound", v)
	}
}

// TestGetPropsUnicodeRejected proves a CP_WINUNICODE GetProps is refused (NSPI
// strings are code-page encoded), consistent with the other ops.
func TestGetPropsUnicodeRejected(t *testing.T) {
	s := testGAL("alice@hermex.test")
	result, _ := decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midBase, codePage: cpWinUnicode}, nil)))
	if result != ecNotSupported {
		t.Errorf("result = %#x, want ecNotSupported", result)
	}
}
