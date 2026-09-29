package ews

import (
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// itemShape is the part of an <m:ItemShape> this server reads beyond the base
// shape: the extended properties a client asks for by field URI.
type itemShape struct {
	Extended []oxews.ExtendedFieldURI `xml:"AdditionalProperties>ExtendedFieldURI"`
}

// extField is one requested extended property: the URI to echo and the property
// it addresses.
type extField struct {
	uri    oxews.ExtendedFieldURI
	target oxews.FieldTarget
}

// extendedFields validates the extended properties a shape asks for and drops a
// repeat of one already asked for, so each property is answered once per item
// however many equivalent spellings named it.
func extendedFields(shape itemShape) ([]extField, error) {
	seen := make(map[string]bool, len(shape.Extended))
	var out []extField
	for _, u := range shape.Extended {
		t, err := u.Target()
		if err != nil {
			return nil, err
		}
		if seen[t.Key()] {
			continue
		}
		seen[t.Key()] = true
		out = append(out, extField{uri: u.Echo(), target: t})
	}
	return out, nil
}

// readExtended reads the requested extended properties of one stored item. A
// property the item does not carry, and a named property the mailbox has never
// seen, is left out, as Exchange leaves out an absent property.
func readExtended(st *objectstore.Store, messageID int64, fields []extField) []oxews.ExtendedProperty {
	if len(fields) == 0 {
		return nil
	}
	tags := make([]mapi.PropTag, len(fields))
	for i, f := range fields {
		tags[i] = fieldTag(st, f.target)
	}
	named := nonZero(tags)
	if len(named) == 0 {
		return nil
	}
	props, err := st.GetMessageProperties(messageID, named...)
	if err != nil {
		return nil
	}
	var out []oxews.ExtendedProperty
	for i, f := range fields {
		if tags[i] == 0 {
			continue
		}
		v, ok := props.Get(tags[i])
		if !ok {
			continue
		}
		if val, vals, ok := oxews.FormatValue(f.target.Type, v); ok {
			out = append(out, oxews.ExtendedProperty{FieldURI: f.uri, Value: val, Values: vals})
		}
	}
	return out
}

// fieldTag resolves a field target to the tag it has in this mailbox, or 0 for a
// named property the mailbox has no id for.
func fieldTag(st *objectstore.Store, t oxews.FieldTarget) mapi.PropTag {
	if !t.Named {
		return mapi.MakeTag(t.ID, t.Type)
	}
	ids, err := st.GetNamedPropIDs(false, []mapi.PropertyName{t.Name})
	if err != nil || ids[0] == 0 {
		return 0
	}
	return mapi.MakeTag(ids[0], t.Type)
}

// nonZero returns the tags that name a property.
func nonZero(tags []mapi.PropTag) []mapi.PropTag {
	out := make([]mapi.PropTag, 0, len(tags))
	for _, t := range tags {
		if t != 0 {
			out = append(out, t)
		}
	}
	return out
}
