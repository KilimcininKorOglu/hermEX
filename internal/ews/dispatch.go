package ews

import (
	"net/http"

	"hermex/internal/logging"
	"hermex/internal/serve"
)

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
	s.Logger.Emit(logging.Event{
		Level:      logging.LevelInfo,
		Subsystem:  logging.EWS,
		Name:       "operation",
		User:       sess.user,
		RemoteAddr: serve.ClientAddr(r),
		Fields:     logging.Fields{"op": op},
	})
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
	case "UpdateItem":
		s.handleUpdateItem(w, inner, sess)
	case "DeleteItem":
		s.handleDeleteItem(w, inner, sess)
	case "MoveItem":
		s.handleMoveItem(w, inner, sess)
	case "CopyItem":
		s.handleCopyItem(w, inner, sess)
	case "CreateFolder":
		s.handleCreateFolder(w, inner, sess)
	case "DeleteFolder":
		s.handleDeleteFolder(w, inner, sess)
	case "GetUserAvailabilityRequest":
		// MS-OXWSAVAIL names the request element GetUserAvailabilityRequest (the
		// "Request" suffix is unlike the other operations' bare names).
		s.handleGetUserAvailability(w, inner, sess)
	case "Subscribe":
		s.handleSubscribe(w, inner, sess)
	case "Unsubscribe":
		s.handleUnsubscribe(w, inner, sess)
	case "GetEvents":
		s.handleGetEvents(w, inner, sess)
	default:
		writeSOAPFault(w, "ErrorInvalidRequest", "unsupported operation: "+op)
	}
}
