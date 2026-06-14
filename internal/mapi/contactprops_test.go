package mapi

import "testing"

// TestContactProptags pins each contact PidTag to its MS-OXPROPS property id and
// type. The point is not that a constant equals itself but that a typo in a hex
// digit (wrong id) or a wrong PtType (e.g. treating the birthday as text) is
// caught: a contact stored under the wrong tag would be invisible to any client
// reading the right one.
func TestContactProptags(t *testing.T) {
	cases := []struct {
		name   string
		tag    PropTag
		wantID uint16
		wantTy PropType
	}{
		{"GivenName", PrGivenName, 0x3A06, PtUnicode},
		{"Surname", PrSurname, 0x3A11, PtUnicode},
		{"MiddleName", PrMiddleName, 0x3A44, PtUnicode},
		{"Nickname", PrNickname, 0x3A4F, PtUnicode},
		{"Title", PrTitle, 0x3A17, PtUnicode},
		{"CompanyName", PrCompanyName, 0x3A16, PtUnicode},
		{"DepartmentName", PrDepartmentName, 0x3A18, PtUnicode},
		{"Birthday", PrBirthday, 0x3A42, PtSysTime},
		{"BusinessTelephone", PrBusinessTelephoneNumber, 0x3A08, PtUnicode},
		{"HomeTelephone", PrHomeTelephoneNumber, 0x3A09, PtUnicode},
		{"MobileTelephone", PrMobileTelephoneNumber, 0x3A1C, PtUnicode},
		{"BusinessFax", PrBusinessFaxNumber, 0x3A24, PtUnicode},
		{"HomeAddressStreet", PrHomeAddressStreet, 0x3A5D, PtUnicode},
		{"HomeAddressCity", PrHomeAddressCity, 0x3A59, PtUnicode},
		{"OtherAddressStreet", PrOtherAddressStreet, 0x3A63, PtUnicode},
		{"BusinessHomePage", PrBusinessHomePage, 0x3A51, PtUnicode},
	}
	for _, c := range cases {
		if c.tag.ID() != c.wantID {
			t.Errorf("%s: id 0x%04X, want 0x%04X", c.name, c.tag.ID(), c.wantID)
		}
		if c.tag.Type() != c.wantTy {
			t.Errorf("%s: type 0x%04X, want 0x%04X", c.name, c.tag.Type(), c.wantTy)
		}
	}
}

// TestContactNamedProps pins the PSETID_Address namespace and the contact named
// properties' LIDs, the (GUID, LID) pair the store's allocator maps to a property
// id. A wrong GUID or LID would resolve a different property than the vCard
// converter intends.
func TestContactNamedProps(t *testing.T) {
	if PsetidAddress.Data1 != 0x00062004 {
		t.Errorf("PsetidAddress Data1 0x%08X, want 0x00062004", PsetidAddress.Data1)
	}
	cases := []struct {
		name    string
		pn      PropertyName
		wantLID uint32
	}{
		{"Email1Address", NameEmail1Address, 0x8083},
		{"Email2Address", NameEmail2Address, 0x8093},
		{"Email3Address", NameEmail3Address, 0x80A3},
		{"WorkAddressStreet", NameWorkAddressStreet, 0x8045},
		{"WorkAddressPostOfficeBox", NameWorkAddressPostOfficeBox, 0x804A},
		{"FileAs", NameFileAs, 0x8005},
		{"InstantMessagingAddress", NameInstantMessagingAddress, 0x8062},
		{"HasPicture", NameHasPicture, 0x8015},
	}
	for _, c := range cases {
		if c.pn.Kind != MnidID {
			t.Errorf("%s: kind %v, want MnidID", c.name, c.pn.Kind)
		}
		if c.pn.LID != c.wantLID {
			t.Errorf("%s: LID 0x%04X, want 0x%04X", c.name, c.pn.LID, c.wantLID)
		}
		if c.pn.GUID != PsetidAddress {
			t.Errorf("%s: GUID %v, want PsetidAddress", c.name, c.pn.GUID)
		}
	}
}
