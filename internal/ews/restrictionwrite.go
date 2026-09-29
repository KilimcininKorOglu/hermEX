package ews

import (
	"encoding/xml"
	"errors"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// errInexpressible is a restriction an EWS search expression cannot state: a
// size, a sub-object or a count restriction, a comparison of two properties, or
// a value no EWS type names. Outlook builds such criteria; EWS has no words for
// them.
var errInexpressible = errors.New("ews: restriction has no EWS form")

// restrictionWriter renders a MAPI restriction as EWS search expressions against
// one mailbox, which gives a named property id its name. It is the inverse of
// restrictionReader: what one reads, the other writes back as the same search.
type restrictionWriter struct {
	st *objectstore.Store
}

// tagFieldURIs are the FieldURIs that name exactly one tagged property and take
// its value as it is stored. Every other property is written as an
// ExtendedFieldURI, which names it exactly.
var tagFieldURIs = uniqueTagFields()

// uniqueTagFields inverts fieldPaths over the fields that name one tag, leaving
// out a tag two fields name and a field whose value is a name.
func uniqueTagFields() map[mapi.PropTag]string {
	out := map[mapi.PropTag]string{}
	uses := map[mapi.PropTag]int{}
	for uri, p := range fieldPaths {
		if _, enum := enumFields[uri]; enum || p.named != nil || len(p.tags) != 1 {
			continue
		}
		out[p.tags[0]] = uri
		uses[p.tags[0]]++
	}
	for tag, n := range uses {
		if n > 1 {
			delete(out, tag)
		}
	}
	return out
}

// write renders one restriction.
func (rw restrictionWriter) write(r mapi.Restriction) (oxews.SearchExpression, error) {
	switch r.Type {
	case mapi.ResAnd, mapi.ResOr:
		return rw.logical(r)
	case mapi.ResNot:
		return writeTyped(r.Value, rw.not)
	case mapi.ResComment:
		return writeTyped(r.Value, rw.comment)
	case mapi.ResContent:
		return writeTyped(r.Value, rw.contains)
	case mapi.ResProperty:
		return writeTyped(r.Value, rw.compare)
	case mapi.ResBitmask:
		return writeTyped(r.Value, rw.bitmask)
	case mapi.ResExist:
		return writeTyped(r.Value, func(e mapi.ExistRestriction) (oxews.SearchExpression, error) {
			return rw.withPath("Exists", e.PropTag)
		})
	}
	return oxews.SearchExpression{}, errInexpressible
}

// writeTyped applies one writer to a restriction value, refusing a value that
// does not hold the shape its type promises.
func writeTyped[T any](v any, write func(T) (oxews.SearchExpression, error)) (oxews.SearchExpression, error) {
	t, ok := v.(T)
	if !ok {
		return oxews.SearchExpression{}, errInexpressible
	}
	return write(t)
}

// comment renders the restriction a comment holds: the comment annotates it and
// does not change what it matches. A comment over nothing matches every message,
// which no EWS expression states.
func (rw restrictionWriter) comment(c mapi.CommentRestriction) (oxews.SearchExpression, error) {
	if c.Res == nil {
		return oxews.SearchExpression{}, errInexpressible
	}
	return rw.write(*c.Res)
}

// logical renders an And or an Or.
func (rw restrictionWriter) logical(r mapi.Restriction) (oxews.SearchExpression, error) {
	name := "And"
	if r.Type == mapi.ResOr {
		name = "Or"
	}
	e := expression(name)
	kids, _ := r.Value.([]mapi.Restriction)
	for _, k := range kids {
		c, err := rw.write(k)
		if err != nil {
			return oxews.SearchExpression{}, err
		}
		e.Children = append(e.Children, c)
	}
	if len(e.Children) == 0 {
		return oxews.SearchExpression{}, errInexpressible
	}
	return e, nil
}

// not renders a Not over one expression.
func (rw restrictionWriter) not(inner mapi.Restriction) (oxews.SearchExpression, error) {
	c, err := rw.write(inner)
	if err != nil {
		return oxews.SearchExpression{}, err
	}
	e := expression("Not")
	e.Children = []oxews.SearchExpression{c}
	return e, nil
}

// contains renders a string test as a Contains.
func (rw restrictionWriter) contains(c mapi.ContentRestriction) (oxews.SearchExpression, error) {
	mode, ok := nameOf(containmentModes, c.FuzzyLevel&0xFFFF)
	cmp, ok2 := nameOf(containmentComparisons, c.FuzzyLevel&^0xFFFF)
	text, ok3 := c.PropVal.Value.(string)
	if !ok || !ok2 || !ok3 {
		return oxews.SearchExpression{}, errInexpressible
	}
	e, err := rw.withPath("Contains", c.PropTag)
	if err != nil {
		return oxews.SearchExpression{}, err
	}
	e.ContainmentMode, e.ContainmentComparison = mode, cmp
	e.Constant = &oxews.ValueAttr{Value: &text}
	return e, nil
}

// compare renders a comparison of a property against a constant.
func (rw restrictionWriter) compare(p mapi.PropertyRestriction) (oxews.SearchExpression, error) {
	name, ok := nameOf(comparisons, p.Relop)
	if !ok {
		return oxews.SearchExpression{}, errInexpressible
	}
	value, _, ok := oxews.FormatValue(p.PropVal.Tag.Type().Base(), p.PropVal.Value)
	if !ok {
		return oxews.SearchExpression{}, errInexpressible
	}
	e, err := rw.withPath(name, p.PropTag)
	if err != nil {
		return oxews.SearchExpression{}, err
	}
	e.FieldURIOrConstant = &oxews.FieldOrConstant{Constant: &oxews.ValueAttr{Value: value}}
	return e, nil
}

// bitmask renders a mask test: an Excludes when the masked bits must be clear,
// and a Not over one when any of them must be set.
func (rw restrictionWriter) bitmask(b mapi.BitmaskRestriction) (oxews.SearchExpression, error) {
	e, err := rw.withPath("Excludes", b.PropTag)
	if err != nil {
		return oxews.SearchExpression{}, err
	}
	mask := strconv.FormatUint(uint64(b.Mask), 10)
	e.Bitmask = &oxews.ValueAttr{Value: &mask}
	if b.Relop == mapi.BmrEqz {
		return e, nil
	}
	not := expression("Not")
	not.Children = []oxews.SearchExpression{e}
	return not, nil
}

// withPath is the named expression over the property tag names.
func (rw restrictionWriter) withPath(name string, tag mapi.PropTag) (oxews.SearchExpression, error) {
	e := expression(name)
	tag = tag.WithType(unicodeType(tag.Type()))
	if uri, ok := tagFieldURIs[tag]; ok {
		e.FieldURI = &oxews.PathToField{URI: uri}
		return e, nil
	}
	t := oxews.FieldTarget{Type: tag.Type(), ID: tag.ID()}
	if tag.ID() >= 0x8000 {
		pn, ok, err := rw.st.NamedPropName(tag.ID())
		if err != nil {
			return oxews.SearchExpression{}, err
		}
		if !ok {
			return oxews.SearchExpression{}, errInexpressible
		}
		t = oxews.FieldTarget{Type: tag.Type(), Named: true, Name: pn}
	}
	u, ok := oxews.FieldURIFor(t)
	if !ok {
		return oxews.SearchExpression{}, errInexpressible
	}
	e.ExtendedFieldURI = &u
	return e, nil
}

// unicodeType is t with an 8-bit string type read as the Unicode one, the only
// string type EWS names.
func unicodeType(t mapi.PropType) mapi.PropType {
	if t.Base() == mapi.PtString8 {
		return t + mapi.PtUnicode - mapi.PtString8
	}
	return t
}

// expression is an empty search expression element of the given name.
func expression(name string) oxews.SearchExpression {
	return oxews.SearchExpression{XMLName: xml.Name{Local: name}}
}

// nameOf is the name a reading table gives value. The empty spelling a reader
// takes as the default is never written.
func nameOf[V comparable](names map[string]V, value V) (string, bool) {
	for name, v := range names {
		if v == value && name != "" {
			return name, true
		}
	}
	return "", false
}
