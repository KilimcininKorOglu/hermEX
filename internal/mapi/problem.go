package mapi

// PropertyProblem reports a per-property failure within a batch operation
// (MS-OXCDATA §2.7 PropertyProblem): the index of the offending property, its
// tag, and the error code.
type PropertyProblem struct {
	Index   uint16
	PropTag PropTag
	Err     uint32
}
