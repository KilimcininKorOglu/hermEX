package ews

import "net/http"

// dispatch parses the SOAP request and routes the operation to its handler.
// Handlers are added per increment; an unrecognized or not-yet-implemented
// operation returns a SOAP Fault (the request never reaches a per-operation
// response message).
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, sess *session) {
	op, _, err := readEnvelope(r)
	if err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "could not parse SOAP envelope: "+err.Error())
		return
	}
	switch op {
	// Operation handlers are added per increment (GetFolder, FindItem,
	// GetItem, SyncFolderItems, CreateItem, ...).
	default:
		writeSOAPFault(w, "ErrorInvalidRequest", "unsupported operation: "+op)
	}
	_ = sess
}
