package ews

import (
	"slices"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// itemShape is the part of an <m:ItemShape> this server reads: the base shape, the
// properties a client adds by field URI, and the extended properties it asks for.
type itemShape struct {
	BaseShape string `xml:"BaseShape"`
	Fields    []struct {
		URI string `xml:"FieldURI,attr"`
	} `xml:"AdditionalProperties>FieldURI"`
	Extended []oxews.ExtendedFieldURI `xml:"AdditionalProperties>ExtendedFieldURI"`
}

// wantsHeaders reports whether a shape asks for InternetMessageHeaders: by name, or
// through AllProperties, which includes them for GetItem.
func (s itemShape) wantsHeaders() bool {
	if s.BaseShape == "AllProperties" {
		return true
	}
	for _, f := range s.Fields {
		if f.URI == "item:InternetMessageHeaders" {
			return true
		}
	}
	return false
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

// extendedValues resolves the extended properties a client sent into the values to
// store. A named property the mailbox has never seen is given an id. The code
// names the first property refused.
func extendedValues(st *objectstore.Store, eps []oxews.ExtendedProperty) (mapi.PropertyValues, string) {
	var out mapi.PropertyValues
	for _, ep := range eps {
		t, err := ep.FieldURI.Target()
		if err != nil {
			return nil, "ErrorInvalidExtendedProperty"
		}
		tag, v, code := extendedValue(st, t, ep)
		if code != "" {
			return nil, code
		}
		out.Set(tag, v)
	}
	return out, ""
}

// extendedValue reads one client-sent value of the property t and the tag it is
// stored under.
func extendedValue(st *objectstore.Store, t oxews.FieldTarget, ep oxews.ExtendedProperty) (mapi.PropTag, any, string) {
	v, err := oxews.ParseValue(t.Type, ep)
	if err != nil {
		return 0, nil, "ErrorInvalidExtendedPropertyValue"
	}
	if !t.Named {
		return mapi.MakeTag(t.ID, t.Type), v, ""
	}
	ids, err := st.GetNamedPropIDs(true, []mapi.PropertyName{t.Name})
	if err != nil || ids[0] == 0 {
		return 0, nil, "ErrorInternalServerError"
	}
	return mapi.MakeTag(ids[0], t.Type), v, ""
}

// extUpdate is what one ItemChange does to extended properties: the values it
// sets and the tags it removes.
type extUpdate struct {
	set    mapi.PropertyValues
	remove []mapi.PropTag
}

func (u extUpdate) empty() bool { return len(u.set) == 0 && len(u.remove) == 0 }

// apply writes the update onto a message's properties.
func (u extUpdate) apply(props *mapi.PropertyValues) {
	for _, pv := range u.set {
		props.Set(pv.Tag, pv.Value)
	}
	for _, tag := range u.remove {
		props.Remove(tag)
	}
}

// extendedUpdate reads the extended-property sets and deletes of one ItemChange.
// The code names the first one refused.
func extendedUpdate(st *objectstore.Store, ch itemChangeReq) (extUpdate, string) {
	var u extUpdate
	for _, sf := range ch.Updates.SetFields {
		if sf.Extended == nil {
			continue
		}
		ep, ok := matchingValue(*sf.Extended, slices.Concat(sf.Message.Extended, sf.Item.Extended, sf.Task.Extended))
		if !ok {
			return extUpdate{}, "ErrorInvalidExtendedProperty"
		}
		vals, code := extendedValues(st, []oxews.ExtendedProperty{ep})
		if code != "" {
			return extUpdate{}, code
		}
		u.set = append(u.set, vals...)
	}
	remove, code := extendedDeletes(st, ch.Updates.DeleteFields)
	u.remove = remove
	return u, code
}

// matchingValue finds the value a SetItemField carries for the property its field
// URI names.
func matchingValue(uri oxews.ExtendedFieldURI, eps []oxews.ExtendedProperty) (oxews.ExtendedProperty, bool) {
	want, err := uri.Target()
	if err != nil {
		return oxews.ExtendedProperty{}, false
	}
	for _, ep := range eps {
		if t, err := ep.FieldURI.Target(); err == nil && t.Key() == want.Key() {
			return ep, true
		}
	}
	return oxews.ExtendedProperty{}, false
}

// extendedDeletes resolves the DeleteItemFields of one ItemChange to the tags to
// remove. A named property the mailbox has never seen is on no item, so it removes
// nothing.
func extendedDeletes(st *objectstore.Store, fields []deleteItemField) ([]mapi.PropTag, string) {
	var out []mapi.PropTag
	for _, df := range fields {
		if df.Extended == nil {
			return nil, "ErrorInvalidPropertyDelete"
		}
		t, err := df.Extended.Target()
		if err != nil {
			return nil, "ErrorInvalidExtendedProperty"
		}
		if tag := fieldTag(st, t); tag != 0 {
			out = append(out, tag)
		}
	}
	return out, ""
}
