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
	case "CreateAttachment":
		s.handleCreateAttachment(w, inner, sess)
	case "SyncFolderItems":
		s.handleSyncFolderItems(w, inner, sess)
	case "CreateItem":
		s.handleCreateItem(w, inner, sess)
	case "SendItem":
		s.handleSendItem(w, inner, sess)
	case "ResolveNames":
		s.handleResolveNames(w, inner, sess)
	case "GetUserPhoto":
		s.handleGetUserPhoto(w, inner, sess)
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
	case "UpdateFolder":
		s.handleUpdateFolder(w, inner, sess)
	case "MoveFolder":
		s.handleMoveFolder(w, inner, sess)
	case "CopyFolder":
		s.handleCopyFolder(w, inner, sess)
	case "GetServerTimeZones":
		s.handleGetServerTimeZones(w, inner, sess)
	case "GetInboxRules":
		s.handleGetInboxRules(w, inner, sess)
	case "UpdateInboxRules":
		s.handleUpdateInboxRules(w, inner, sess)
	case "GetDelegate":
		s.handleGetDelegate(w, inner, sess)
	case "AddDelegate":
		s.handleAddDelegate(w, inner, sess)
	case "RemoveDelegate":
		s.handleRemoveDelegate(w, inner, sess)
	case "UpdateDelegate":
		s.handleUpdateDelegate(w, inner, sess)
	case "GetUserAvailabilityRequest":
		// MS-OXWSAVAIL names the request element GetUserAvailabilityRequest (the
		// "Request" suffix is unlike the other operations' bare names).
		s.handleGetUserAvailability(w, inner, sess)
	case "GetUserOofSettingsRequest":
		// MS-OXWSOOF likewise names its request elements with a "Request" suffix.
		s.handleGetUserOofSettings(w, inner, sess)
	case "SetUserOofSettingsRequest":
		s.handleSetUserOofSettings(w, inner, sess)
	case "GetMailTips":
		s.handleGetMailTips(w, inner, sess)
	case "ExpandDL":
		s.handleExpandDL(w, inner, sess)
	case "EmptyFolder":
		s.handleEmptyFolder(w, inner, sess)
	case "MarkAllItemsAsRead":
		s.handleMarkAllItemsAsRead(w, inner, sess)
	case "DeleteAttachment":
		s.handleDeleteAttachment(w, inner, sess)
	case "MarkAsJunk":
		s.handleMarkAsJunk(w, inner, sess)
	case "FindConversation":
		s.handleFindConversation(w, inner, sess)
	case "GetConversationItems":
		s.handleGetConversationItems(w, inner, sess)
	case "ApplyConversationAction":
		s.handleApplyConversationAction(w, inner, sess)
	case "GetUserConfiguration":
		s.handleGetUserConfiguration(w, inner, sess)
	case "CreateUserConfiguration":
		s.handleCreateUserConfiguration(w, inner, sess)
	case "UpdateUserConfiguration":
		s.handleUpdateUserConfiguration(w, inner, sess)
	case "DeleteUserConfiguration":
		s.handleDeleteUserConfiguration(w, inner, sess)
	case "Subscribe":
		s.handleSubscribe(w, inner, sess)
	case "Unsubscribe":
		s.handleUnsubscribe(w, inner, sess)
	case "GetEvents":
		s.handleGetEvents(w, inner, sess)
	case "GetStreamingEvents":
		// Streaming holds the connection open and writes chunked continuations, so
		// it needs the request (its context signals client disconnect).
		s.handleGetStreamingEvents(w, r, inner, sess)
	default:
		writeSOAPFault(w, "ErrorInvalidRequest", "unsupported operation: "+op)
	}
}
