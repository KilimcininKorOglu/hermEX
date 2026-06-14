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
	case "FindItem":
		s.handleFindItem(w, inner, sess)
	case "GetItem":
		s.handleGetItem(w, inner, sess)
	case "GetAttachment":
		s.handleGetAttachment(w, inner, sess)
	case "SyncFolderItems":
		s.handleSyncFolderItems(w, inner, sess)
	case "CreateItem":
		s.handleCreateItem(w, inner, sess)
	case "ResolveNames":
		s.handleResolveNames(w, inner, sess)
	// UpdateItem, DeleteItem are added in a later increment.
	default:
		writeSOAPFault(w, "ErrorInvalidRequest", "unsupported operation: "+op)
	}
}
