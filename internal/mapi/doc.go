// Package mapi defines the logical MAPI object and property model that the
// external Exchange client protocols (ROP, NSPI, EWS) dictate: property tags
// and types, named-property identifiers, object types, and MAPI error codes.
//
// This model is mandatory and not subject to redesign even though hermEX's
// physical store schema is original; the wire-visible property value encoding
// in package ext depends on the type semantics defined here.
package mapi
