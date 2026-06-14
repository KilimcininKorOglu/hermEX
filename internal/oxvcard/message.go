package oxvcard

import "hermex/internal/mapi"

// PropIDResolver resolves named properties to store property ids. With create
// true (used by Import) names not yet known are allocated; the result is
// parallel to names, with 0 for an unresolved name. It is satisfied by the
// store's named-property allocator and has the same shape as oxcmail's resolver.
type PropIDResolver func(create bool, names []mapi.PropertyName) ([]uint16, error)

// Options configures a conversion. Resolver supplies named-property ids and is
// required: a contact's email slots, work address, file-as, IM address, and
// has-picture flag are all named properties.
type Options struct {
	Resolver PropIDResolver
}

// namedField is one contact named property and the type its value takes.
type namedField struct {
	name mapi.PropertyName
	typ  mapi.PropType
}

// contactNamed is the fixed set of contact named properties oxvcard maps,
// resolved as a batch through the resolver. Order is irrelevant — results are
// keyed by PropertyName.
var contactNamed = []namedField{
	{mapi.NameEmail1Address, mapi.PtUnicode},
	{mapi.NameEmail1DisplayName, mapi.PtUnicode},
	{mapi.NameEmail1AddressType, mapi.PtUnicode},
	{mapi.NameEmail2Address, mapi.PtUnicode},
	{mapi.NameEmail2DisplayName, mapi.PtUnicode},
	{mapi.NameEmail2AddressType, mapi.PtUnicode},
	{mapi.NameEmail3Address, mapi.PtUnicode},
	{mapi.NameEmail3DisplayName, mapi.PtUnicode},
	{mapi.NameEmail3AddressType, mapi.PtUnicode},
	{mapi.NameWorkAddressStreet, mapi.PtUnicode},
	{mapi.NameWorkAddressCity, mapi.PtUnicode},
	{mapi.NameWorkAddressState, mapi.PtUnicode},
	{mapi.NameWorkAddressPostalCode, mapi.PtUnicode},
	{mapi.NameWorkAddressCountry, mapi.PtUnicode},
	{mapi.NameWorkAddressPostOfficeBox, mapi.PtUnicode},
	{mapi.NameFileAs, mapi.PtUnicode},
	{mapi.NameInstantMessagingAddress, mapi.PtUnicode},
	{mapi.NameHasPicture, mapi.PtBoolean},
}

// namedTags resolves contactNamed to full store proptags. With create the
// allocator assigns fresh ids for names never seen; without it, a name never
// written maps to nothing and is absent from the result. The returned map keys
// are PropertyNames; callers look up the tags they need.
func namedTags(opt Options, create bool) (map[mapi.PropertyName]mapi.PropTag, error) {
	names := make([]mapi.PropertyName, len(contactNamed))
	for i, f := range contactNamed {
		names[i] = f.name
	}
	ids, err := opt.Resolver(create, names)
	if err != nil {
		return nil, err
	}
	out := make(map[mapi.PropertyName]mapi.PropTag, len(contactNamed))
	for i, f := range contactNamed {
		if ids[i] != 0 {
			out[f.name] = mapi.PropTag(uint32(ids[i])<<16 | uint32(f.typ))
		}
	}
	return out, nil
}
