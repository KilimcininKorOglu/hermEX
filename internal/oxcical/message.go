package oxcical

import "hermex/internal/mapi"

// PropIDResolver resolves named properties to store property ids. With create
// true (used by Import) names not yet known are allocated; the result is parallel
// to names, with 0 for an unresolved name. It is satisfied by the store's
// named-property allocator and has the same shape as oxcmail's/oxvcard's resolver.
type PropIDResolver func(create bool, names []mapi.PropertyName) ([]uint16, error)

// Options configures a conversion. Resolver supplies named-property ids and is
// required: an appointment's start/end, location, busy status, subtype, sequence,
// reminder, and preserved UID are all named properties.
type Options struct {
	Resolver PropIDResolver
}

// namedField is one appointment named property and the type its value takes.
type namedField struct {
	name mapi.PropertyName
	typ  mapi.PropType
}

// nameICalUID preserves the iCalendar UID as a named string property (a neutral
// name in the public-strings namespace) so Export can re-emit a stable UID on the
// synthesized path. (Recurring events carry the UID inside their verbatim bytes.)
var nameICalUID = mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsPublicStrings, Name: "ICalUID"}

// appointmentNamed is the fixed set of calendar named properties oxcical maps,
// resolved as a batch through the resolver. Order is irrelevant — results are
// keyed by PropertyName.
var appointmentNamed = []namedField{
	{mapi.NameAppointmentLocation, mapi.PtUnicode},
	{mapi.NameAppointmentStartWhole, mapi.PtSysTime},
	{mapi.NameAppointmentEndWhole, mapi.PtSysTime},
	{mapi.NameAppointmentSubType, mapi.PtBoolean},
	{mapi.NameBusyStatus, mapi.PtLong},
	{mapi.NameAppointmentSequence, mapi.PtLong},
	{mapi.NameReminderSet, mapi.PtBoolean},
	{mapi.NameReminderDelta, mapi.PtLong},
}

// namedTags resolves appointmentNamed to full store proptags. With create the
// allocator assigns fresh ids for names never seen; without it, a name never
// written maps to nothing and is absent from the result. The returned map keys are
// PropertyNames; callers look up the tags they need.
func namedTags(opt Options, create bool) (map[mapi.PropertyName]mapi.PropTag, error) {
	names := make([]mapi.PropertyName, len(appointmentNamed))
	for i, f := range appointmentNamed {
		names[i] = f.name
	}
	ids, err := opt.Resolver(create, names)
	if err != nil {
		return nil, err
	}
	out := make(map[mapi.PropertyName]mapi.PropTag, len(appointmentNamed))
	for i, f := range appointmentNamed {
		if ids[i] != 0 {
			out[f.name] = mapi.PropTag(uint32(ids[i])<<16 | uint32(f.typ))
		}
	}
	return out, nil
}

// resolveOne resolves a single named property to its full proptag (0 if unknown).
func resolveOne(opt Options, name mapi.PropertyName, typ mapi.PropType, create bool) (mapi.PropTag, error) {
	ids, err := opt.Resolver(create, []mapi.PropertyName{name})
	if err != nil {
		return 0, err
	}
	if ids[0] == 0 {
		return 0, nil
	}
	return mapi.PropTag(uint32(ids[0])<<16 | uint32(typ)), nil
}
