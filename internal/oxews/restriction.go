package oxews

import "encoding/xml"

// Restriction is the EWS <Restriction> element ([MS-OXWSSRCH]): one
// search expression. A request carries it in the messages namespace and a
// search folder's parameters in the types namespace; the children read and
// render the same way in both.
type Restriction struct {
	Expr []SearchExpression `xml:",any"`
}

// SearchExpression is one search expression element. And, Or and Not carry
// further expressions; every other expression names a property by one path
// element and may carry a constant, a second path or a bitmask. The element
// order follows the schema of each expression: the path, then its operand.
type SearchExpression struct {
	XMLName               xml.Name
	ContainmentMode       string             `xml:"ContainmentMode,attr,omitempty"`
	ContainmentComparison string             `xml:"ContainmentComparison,attr,omitempty"`
	FieldURI              *PathToField       `xml:"FieldURI"`
	IndexedFieldURI       *PathToField       `xml:"IndexedFieldURI"`
	ExtendedFieldURI      *ExtendedFieldURI  `xml:"ExtendedFieldURI"`
	Constant              *ValueAttr         `xml:"Constant"`
	Bitmask               *ValueAttr         `xml:"Bitmask"`
	FieldURIOrConstant    *FieldOrConstant   `xml:"FieldURIOrConstant"`
	Children              []SearchExpression `xml:",any"`
}

// PathToField is a <FieldURI> or an <IndexedFieldURI>: a property named by its
// EWS field URI.
type PathToField struct {
	URI string `xml:"FieldURI,attr"`
}

// ValueAttr is an element whose one attribute is its value: a <Constant> or a
// <Bitmask>.
type ValueAttr struct {
	Value *string `xml:"Value,attr"`
}

// FieldOrConstant is the <FieldURIOrConstant> a comparison tests its path against.
type FieldOrConstant struct {
	FieldURI         *PathToField      `xml:"FieldURI"`
	ExtendedFieldURI *ExtendedFieldURI `xml:"ExtendedFieldURI"`
	Constant         *ValueAttr        `xml:"Constant"`
}

// SearchParameters is the <t:SearchParameters> of a search folder: what it
// searches for and the folders it searches (SearchParametersType).
type SearchParameters struct {
	Traversal     string      `xml:"Traversal,attr"`
	Restriction   Restriction `xml:"Restriction"`
	BaseFolderIDs []FolderID  `xml:"BaseFolderIds>FolderId"`
}
