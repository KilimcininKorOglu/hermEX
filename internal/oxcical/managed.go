package oxcical

import (
	"slices"

	"hermex/internal/mapi"
)

// managedFixed are the property tags Import writes under a fixed tag.
var managedFixed = []mapi.PropTag{
	mapi.PrMessageClass,
	mapi.PrSubject,
	mapi.PrBody,
	mapi.PrIcalOriginal,
	mapi.PrSensitivity,
	mapi.PrImportance,
	mapi.PrSentRepresentingSmtpAddress,
	mapi.PrSentRepresentingEmailAddress,
	mapi.PrSentRepresentingAddrType,
	mapi.PrSentRepresentingName,
}

// ManagedTags returns every property tag Import can write, resolved without
// allocating: a name the store never saw has no stored value to manage. An edit
// that replaces a stored appointment with a fresh Import result deletes the
// managed tags the new result lacks and keeps every other property, so a field
// the iCalendar body dropped is cleared while what another client stored stays.
// Recipients are not properties and are not in the set.
func ManagedTags(opt Options) ([]mapi.PropTag, error) {
	named, err := namedTags(opt, false)
	if err != nil {
		return nil, err
	}
	uidTag, err := resolveOne(opt, nameICalUID, mapi.PtUnicode, false)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(managedFixed)
	for _, f := range appointmentNamed {
		if tag, ok := named[f.name]; ok {
			out = append(out, tag)
		}
	}
	if uidTag != 0 {
		out = append(out, uidTag)
	}
	return out, nil
}

// JournalManagedTags is ManagedTags for ImportVJournal: the tags it can write,
// resolved without allocating.
func JournalManagedTags(opt Options) ([]mapi.PropTag, error) {
	uidTag, err := resolveOne(opt, nameICalUID, mapi.PtUnicode, false)
	if err != nil {
		return nil, err
	}
	out := []mapi.PropTag{mapi.PrMessageClass, mapi.PrSubject, mapi.PrBody, mapi.PrIcalOriginal}
	if uidTag != 0 {
		out = append(out, uidTag)
	}
	return out, nil
}
