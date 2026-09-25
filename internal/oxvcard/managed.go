package oxvcard

import (
	"slices"

	"hermex/internal/mapi"
)

// managedFixed are the property tags Import writes under a fixed tag.
var managedFixed = []mapi.PropTag{
	mapi.PrMessageClass,
	mapi.PrDisplayName,
	mapi.PrSurname,
	mapi.PrGivenName,
	mapi.PrMiddleName,
	mapi.PrDisplayNamePrefix,
	mapi.PrGeneration,
	mapi.PrNickname,
	mapi.PrBirthday,
	mapi.PrTitle,
	mapi.PrProfession,
	mapi.PrCompanyName,
	mapi.PrDepartmentName,
	mapi.PrBody,
	mapi.PrPersonalHomePage,
	mapi.PrBusinessHomePage,
	mapi.PrHomeFaxNumber,
	mapi.PrBusinessFaxNumber,
	mapi.PrMobileTelephoneNumber,
	mapi.PrPagerTelephoneNumber,
	mapi.PrCarTelephoneNumber,
	mapi.PrHomeTelephoneNumber,
	mapi.PrBusinessTelephoneNumber,
	mapi.PrOtherTelephoneNumber,
	mapi.PrHomeAddressStreet,
	mapi.PrHomeAddressCity,
	mapi.PrHomeAddressStateOrProvince,
	mapi.PrHomeAddressPostalCode,
	mapi.PrHomeAddressCountry,
	mapi.PrHomeAddressPostOfficeBox,
	mapi.PrOtherAddressStreet,
	mapi.PrOtherAddressCity,
	mapi.PrOtherAddressStateOrProvince,
	mapi.PrOtherAddressPostalCode,
	mapi.PrOtherAddressCountry,
	mapi.PrOtherAddressPostOfficeBox,
}

// ManagedTags returns every property tag Import can write, resolved without
// allocating: a name the store never saw has no stored value to manage. An edit
// that replaces a stored contact with a fresh Import result deletes the managed
// tags the new result lacks and keeps every other property, so a field the vCard
// dropped is cleared while what another client stored stays. The file-as name is
// not managed: Import never writes it, and Outlook and webmail set it.
// Attachments are not properties; the photo attachment is the caller's to keep or
// replace.
func ManagedTags(opt Options) ([]mapi.PropTag, error) {
	named, err := namedTags(opt, false)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(managedFixed)
	for _, f := range contactNamed {
		if tag, ok := named[f.name]; ok && f.name != mapi.NameFileAs {
			out = append(out, tag)
		}
	}
	for _, n := range []namedField{{nameVCardUID, mapi.PtUnicode}, {mapi.NameKeywords, mapi.PtMvUnicode}} {
		tag, err := resolveOne(opt, n.name, n.typ, false)
		if err != nil {
			return nil, err
		}
		if tag != 0 {
			out = append(out, tag)
		}
	}
	return out, nil
}
