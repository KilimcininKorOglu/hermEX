package mapi

// SVREID is a server entry identifier (MS-OXCDATA §2.2.1.2 ServerEid). It comes
// in two forms, discriminated by a one-byte flag on the wire:
//
//   - When Bin is non-nil (including an empty slice), it is the binary form: an
//     opaque byte string.
//   - When Bin is nil, it is the long-term form: a folder id, a message id, and
//     an instance number.
type SVREID struct {
	Bin       []byte // non-nil selects the binary form
	FolderID  EID
	MessageID EID
	Instance  uint32
}
