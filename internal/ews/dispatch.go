package ews

import "net/http"

// dispatch parses the SOAP request and routes the operation to its handler.
// Handlers are added per increment; an unrecognized or not-yet-implemented
// operation returns a SOAP Fault (the request never reaches a per-operation
// response message).
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, sess *session) {
	op, inner, err := readEnvelope(r)
	if err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "could not parse SOAP envelope: "+err.Error())
		return
	}
	switch op {
	case "GetFolder":
		s.handleGetFolder(w, inner, sess)
	case "FindFolder":
		s.handleFindFolder(w, inner, sess)
	case "SyncFolderHierarchy":
		s.handleSyncFolderHierarchy(w, inner, sess)
	// Item operations (FindItem, GetItem, SyncFolderItems, CreateItem, ...) are
	// added in later increments.
	default:
		writeSOAPFault(w, "ErrorInvalidRequest", "unsupported operation: "+op)
	}
}
