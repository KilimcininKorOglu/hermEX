package ews

import (
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// An EWS <m:Restriction> ([MS-OXWSSRCH] 2.2.4.x) is read into the MAPI
// restriction the store evaluates, so a FindItem filter and a search folder's
// criteria are one kind of object every protocol reads the same way.

// Response codes a restriction is refused with.
const (
	codeInvalidRestriction = "ErrorInvalidRestriction"
	codeUnsupportedPath    = "ErrorUnsupportedPathForQuery"
)

// Fuzzy-level bits of a content restriction ([MS-OXCDATA] 2.12.3).
const (
	fuzzyFullString    = 0x00000000
	fuzzySubstring     = 0x00000001
	fuzzyPrefix        = 0x00000002
	fuzzyPrefixOnWords = 0x00000010
	fuzzyPhrase        = 0x00000020
	fuzzyIgnoreCase    = 0x00010000
	fuzzyIgnoreNonSp   = 0x00020000
	fuzzyLoose         = 0x00040000
)

// comparisons are the comparison elements and the operators they stand for.
var comparisons = map[string]mapi.Relop{
	"IsEqualTo":              mapi.RelopEQ,
	"IsNotEqualTo":           mapi.RelopNE,
	"IsGreaterThan":          mapi.RelopGT,
	"IsGreaterThanOrEqualTo": mapi.RelopGE,
	"IsLessThan":             mapi.RelopLT,
	"IsLessThanOrEqualTo":    mapi.RelopLE,
}

// containmentModes are the ContainmentMode values and the match kinds they name.
var containmentModes = map[string]uint32{
	"":              fuzzyFullString,
	"FullString":    fuzzyFullString,
	"Prefixed":      fuzzyPrefix,
	"Substring":     fuzzySubstring,
	"PrefixOnWords": fuzzyPrefixOnWords,
	"ExactPhrase":   fuzzyPhrase,
}

// containmentComparisons are the ContainmentComparison values and the flags they
// add.
var containmentComparisons = map[string]uint32{
	"":                                    0,
	"Exact":                               0,
	"IgnoreCase":                          fuzzyIgnoreCase,
	"IgnoreNonSpacingCharacters":          fuzzyIgnoreNonSp,
	"Loose":                               fuzzyLoose,
	"IgnoreCaseAndNonSpacingCharacters":   fuzzyIgnoreCase | fuzzyIgnoreNonSp,
	"LooseAndIgnoreCase":                  fuzzyLoose | fuzzyIgnoreCase,
	"LooseAndIgnoreNonSpace":              fuzzyLoose | fuzzyIgnoreNonSp,
	"LooseAndIgnoreCaseAndIgnoreNonSpace": fuzzyLoose | fuzzyIgnoreCase | fuzzyIgnoreNonSp,
}

// restrictionReader reads search expressions against one mailbox, which gives
// a named property its id.
type restrictionReader struct {
	st *objectstore.Store
}

// read reads a <m:Restriction> into a MAPI restriction. A nil element reads as
// no restriction. The code names why a restriction is refused.
func (rr restrictionReader) read(r *oxews.Restriction) (*mapi.Restriction, string) {
	if r == nil {
		return nil, ""
	}
	if len(r.Expr) != 1 {
		return nil, codeInvalidRestriction
	}
	out, code := rr.expr(r.Expr[0])
	if code != "" {
		return nil, code
	}
	return &out, ""
}

// expr reads one search expression.
func (rr restrictionReader) expr(e oxews.SearchExpression) (mapi.Restriction, string) {
	switch e.XMLName.Local {
	case "And", "Or":
		return rr.logical(e)
	case "Not":
		return rr.not(e)
	case "Contains":
		return rr.contains(e)
	case "Excludes":
		return rr.excludes(e)
	case "Exists":
		return rr.exists(e)
	}
	if op, ok := comparisons[e.XMLName.Local]; ok {
		return rr.compare(e, op)
	}
	return mapi.Restriction{}, codeInvalidRestriction
}

// logical reads an And or an Or over its child expressions.
func (rr restrictionReader) logical(e oxews.SearchExpression) (mapi.Restriction, string) {
	if len(e.Children) == 0 {
		return mapi.Restriction{}, codeInvalidRestriction
	}
	kids := make([]mapi.Restriction, 0, len(e.Children))
	for _, c := range e.Children {
		k, code := rr.expr(c)
		if code != "" {
			return mapi.Restriction{}, code
		}
		kids = append(kids, k)
	}
	typ := mapi.ResAnd
	if e.XMLName.Local == "Or" {
		typ = mapi.ResOr
	}
	return mapi.Restriction{Type: typ, Value: kids}, ""
}

// not reads a Not over its one child expression.
func (rr restrictionReader) not(e oxews.SearchExpression) (mapi.Restriction, string) {
	if len(e.Children) != 1 {
		return mapi.Restriction{}, codeInvalidRestriction
	}
	inner, code := rr.expr(e.Children[0])
	if code != "" {
		return mapi.Restriction{}, code
	}
	return mapi.Restriction{Type: mapi.ResNot, Value: inner}, ""
}

// contains reads a Contains: a string test of each property the path names.
func (rr restrictionReader) contains(e oxews.SearchExpression) (mapi.Restriction, string) {
	mode, ok := containmentModes[e.ContainmentMode]
	flags, ok2 := containmentComparisons[e.ContainmentComparison]
	if !ok || !ok2 || e.Constant == nil || e.Constant.Value == nil {
		return mapi.Restriction{}, codeInvalidRestriction
	}
	tags, code := rr.path(e.FieldURI, e.IndexedFieldURI, e.ExtendedFieldURI)
	if code != "" {
		return mapi.Restriction{}, code
	}
	var terms []mapi.Restriction
	for _, tag := range tags {
		if tag.Type().Base() != mapi.PtUnicode && tag.Type().Base() != mapi.PtString8 {
			return mapi.Restriction{}, codeInvalidRestriction
		}
		terms = append(terms, mapi.Restriction{Type: mapi.ResContent, Value: mapi.ContentRestriction{
			FuzzyLevel: mode | flags,
			PropTag:    tag,
			PropVal:    mapi.TaggedPropVal{Tag: tag.WithType(mapi.PtUnicode), Value: *e.Constant.Value},
		}})
	}
	return anyOf(terms), ""
}

// excludes reads an Excludes: the bits of the mask are all clear in the one
// integer property the path names.
func (rr restrictionReader) excludes(e oxews.SearchExpression) (mapi.Restriction, string) {
	if e.Bitmask == nil || e.Bitmask.Value == nil {
		return mapi.Restriction{}, codeInvalidRestriction
	}
	mask, err := strconv.ParseUint(*e.Bitmask.Value, 0, 32)
	if err != nil {
		return mapi.Restriction{}, codeInvalidRestriction
	}
	tags, code := rr.path(e.FieldURI, e.IndexedFieldURI, e.ExtendedFieldURI)
	if code != "" {
		return mapi.Restriction{}, code
	}
	if len(tags) != 1 || tags[0].Type() != mapi.PtLong {
		return mapi.Restriction{}, codeInvalidRestriction
	}
	return mapi.Restriction{Type: mapi.ResBitmask, Value: mapi.BitmaskRestriction{
		Relop: mapi.BmrEqz, PropTag: tags[0], Mask: uint32(mask),
	}}, ""
}

// exists reads an Exists: the item carries a property the path names.
func (rr restrictionReader) exists(e oxews.SearchExpression) (mapi.Restriction, string) {
	tags, code := rr.path(e.FieldURI, e.IndexedFieldURI, e.ExtendedFieldURI)
	if code != "" {
		return mapi.Restriction{}, code
	}
	terms := make([]mapi.Restriction, 0, len(tags))
	for _, tag := range tags {
		terms = append(terms, mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{PropTag: tag}})
	}
	return anyOf(terms), ""
}

// compare reads a comparison of the path's properties against a constant. A
// field of several properties matches when one of them compares true, and for
// IsNotEqualTo when none of them equals the constant.
func (rr restrictionReader) compare(e oxews.SearchExpression, op mapi.Relop) (mapi.Restriction, string) {
	other := e.FieldURIOrConstant
	if other == nil || other.Constant == nil || other.Constant.Value == nil {
		// A comparison of two properties is not one a search folder or a
		// FindItem filter needs, and the store does not evaluate it.
		return mapi.Restriction{}, codeInvalidRestriction
	}
	tags, code := rr.path(e.FieldURI, e.IndexedFieldURI, e.ExtendedFieldURI)
	if code != "" {
		return mapi.Restriction{}, code
	}
	terms := make([]mapi.Restriction, 0, len(tags))
	for _, tag := range tags {
		v, ok := constantValue(e.FieldURI, tag.Type().Base(), *other.Constant.Value)
		if !ok {
			return mapi.Restriction{}, codeInvalidRestriction
		}
		terms = append(terms, mapi.Restriction{Type: mapi.ResProperty, Value: mapi.PropertyRestriction{
			Relop: op, PropTag: tag, PropVal: mapi.TaggedPropVal{Tag: tag.WithType(tag.Type().Base()), Value: v},
		}})
	}
	if op == mapi.RelopNE {
		return allOf(terms), ""
	}
	return anyOf(terms), ""
}

// constantValue reads a comparison's constant as a value of type typ. A field
// whose EWS value is a name (the importance, the sensitivity) takes the name.
func constantValue(field *oxews.PathToField, typ mapi.PropType, text string) (any, bool) {
	if field != nil {
		if names, ok := enumFields[field.URI]; ok {
			v, ok := names[text]
			return v, ok
		}
	}
	v, err := oxews.ParseValue(typ, oxews.ExtendedProperty{Value: &text})
	return v, err == nil
}

// path resolves the one path element an expression carries to the tags it
// names in this mailbox.
func (rr restrictionReader) path(field, indexed *oxews.PathToField, ext *oxews.ExtendedFieldURI) ([]mapi.PropTag, string) {
	switch {
	case field != nil && indexed == nil && ext == nil:
		return rr.fieldTags(field.URI)
	case ext != nil && field == nil && indexed == nil:
		return rr.extendedTag(*ext)
	case indexed != nil:
		// An indexed field (a phone number or an e-mail slot of a contact) names
		// one entry of a dictionary this server keeps as separate properties.
		return nil, codeUnsupportedPath
	}
	return nil, codeInvalidRestriction
}

// fieldTags resolves a FieldURI.
func (rr restrictionReader) fieldTags(uri string) ([]mapi.PropTag, string) {
	p, ok := fieldPaths[uri]
	if !ok {
		return nil, codeUnsupportedPath
	}
	if p.named == nil {
		return p.tags, ""
	}
	return rr.namedTag(*p.named, p.typ)
}

// extendedTag resolves an ExtendedFieldURI.
func (rr restrictionReader) extendedTag(u oxews.ExtendedFieldURI) ([]mapi.PropTag, string) {
	t, err := u.Target()
	if err != nil {
		return nil, codeInvalidRestriction
	}
	if !t.Named {
		return []mapi.PropTag{mapi.MakeTag(t.ID, t.Type)}, ""
	}
	return rr.namedTag(t.Name, t.Type)
}

// namedTag gives a named property its id in this mailbox. A name no item has
// carried yet is given one, so a search for it is well formed and finds nothing
// until an item carries it.
func (rr restrictionReader) namedTag(name mapi.PropertyName, typ mapi.PropType) ([]mapi.PropTag, string) {
	ids, err := rr.st.GetNamedPropIDs(true, []mapi.PropertyName{name})
	if err != nil || len(ids) != 1 || ids[0] == 0 {
		return nil, "ErrorInternalServerError"
	}
	return []mapi.PropTag{mapi.MakeTag(ids[0], typ)}, ""
}

// anyOf is the one term, or an Or of several.
func anyOf(terms []mapi.Restriction) mapi.Restriction {
	if len(terms) == 1 {
		return terms[0]
	}
	return mapi.Restriction{Type: mapi.ResOr, Value: terms}
}

// allOf is the one term, or an And of several.
func allOf(terms []mapi.Restriction) mapi.Restriction {
	if len(terms) == 1 {
		return terms[0]
	}
	return mapi.Restriction{Type: mapi.ResAnd, Value: terms}
}
