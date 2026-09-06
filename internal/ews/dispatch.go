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
	op, inner, imp, err := readEnvelope(r)
	if err != nil {
		s.soapFault(w, "ErrorInvalidRequest", "could not parse SOAP envelope: invalid request", err)
		return
	}
	// An ExchangeImpersonation header is gated and applied before the operation: on
	// success it swaps the session to the target, on failure it has already written
	// a SOAP Fault and the operation never runs.
	if !s.applyImpersonation(w, sess, imp) {
		return
	}
	s.Logger.Emit(logging.Event{
		Level:      logging.LevelInfo,
		Subsystem:  logging.EWS,
		Name:       "operation",
		User:       sess.realUser,
		RemoteAddr: serve.ClientAddr(r),
		Fields:     operationFields(op, sess),
	})
	switch op {
	case "GetStreamingEvents":
		// Streaming holds the connection open and writes chunked continuations, so
		// it needs the request (its context signals client disconnect).
		s.handleGetStreamingEvents(w, r, inner, sess)
		return
	case "GetAppMarketplaceUrl":
		// The marketplace URL is a constant, so it reads nothing from the request.
		s.handleGetAppMarketplaceURL(w)
		return
	}
	handler, ok := ewsOperations[op]
	if !ok {
		writeSOAPFault(w, "ErrorInvalidRequest", "unsupported operation: "+op)
		return
	}
	handler(s, w, inner, sess)
}

// ewsOperations is the single routing source from a SOAP operation name to its
// handler. A name absent here is an operation this server does not implement, and
// the two operations with their own signature are routed before this table.
//
// MS-OXWSAVAIL and MS-OXWSOOF name their request elements with a "Request"
// suffix, unlike every other operation's bare name.
var ewsOperations = map[string]func(*Server, http.ResponseWriter, []byte, *session){
	"GetFolder":                  (*Server).handleGetFolder,
	"FindFolder":                 (*Server).handleFindFolder,
	"SyncFolderHierarchy":        (*Server).handleSyncFolderHierarchy,
	"FindItem":                   (*Server).handleFindItem,
	"GetItem":                    (*Server).handleGetItem,
	"GetAttachment":              (*Server).handleGetAttachment,
	"CreateAttachment":           (*Server).handleCreateAttachment,
	"SyncFolderItems":            (*Server).handleSyncFolderItems,
	"CreateItem":                 (*Server).handleCreateItem,
	"SendItem":                   (*Server).handleSendItem,
	"ResolveNames":               (*Server).handleResolveNames,
	"GetUserPhoto":               (*Server).handleGetUserPhoto,
	"UpdateItem":                 (*Server).handleUpdateItem,
	"DeleteItem":                 (*Server).handleDeleteItem,
	"MoveItem":                   (*Server).handleMoveItem,
	"CopyItem":                   (*Server).handleCopyItem,
	"CreateFolder":               (*Server).handleCreateFolder,
	"DeleteFolder":               (*Server).handleDeleteFolder,
	"UpdateFolder":               (*Server).handleUpdateFolder,
	"MoveFolder":                 (*Server).handleMoveFolder,
	"CopyFolder":                 (*Server).handleCopyFolder,
	"GetServerTimeZones":         (*Server).handleGetServerTimeZones,
	"GetInboxRules":              (*Server).handleGetInboxRules,
	"UpdateInboxRules":           (*Server).handleUpdateInboxRules,
	"GetDelegate":                (*Server).handleGetDelegate,
	"AddDelegate":                (*Server).handleAddDelegate,
	"RemoveDelegate":             (*Server).handleRemoveDelegate,
	"UpdateDelegate":             (*Server).handleUpdateDelegate,
	"GetUserAvailabilityRequest": (*Server).handleGetUserAvailability,
	"GetUserOofSettingsRequest":  (*Server).handleGetUserOofSettings,
	"SetUserOofSettingsRequest":  (*Server).handleSetUserOofSettings,
	"GetMailTips":                (*Server).handleGetMailTips,
	"ExpandDL":                   (*Server).handleExpandDL,
	"EmptyFolder":                (*Server).handleEmptyFolder,
	"MarkAllItemsAsRead":         (*Server).handleMarkAllItemsAsRead,
	"DeleteAttachment":           (*Server).handleDeleteAttachment,
	"MarkAsJunk":                 (*Server).handleMarkAsJunk,
	"FindConversation":           (*Server).handleFindConversation,
	"GetConversationItems":       (*Server).handleGetConversationItems,
	"ApplyConversationAction":    (*Server).handleApplyConversationAction,
	"GetUserConfiguration":       (*Server).handleGetUserConfiguration,
	"CreateUserConfiguration":    (*Server).handleCreateUserConfiguration,
	"UpdateUserConfiguration":    (*Server).handleUpdateUserConfiguration,
	"DeleteUserConfiguration":    (*Server).handleDeleteUserConfiguration,
	"ConvertId":                  (*Server).handleConvertId,
	"FindPeople":                 (*Server).handleFindPeople,
	"GetPersona":                 (*Server).handleGetPersona,
	"GetRoomLists":               (*Server).handleGetRoomLists,
	"GetRooms":                   (*Server).handleGetRooms,
	"Subscribe":                  (*Server).handleSubscribe,
	"Unsubscribe":                (*Server).handleUnsubscribe,
	"GetEvents":                  (*Server).handleGetEvents,
}
