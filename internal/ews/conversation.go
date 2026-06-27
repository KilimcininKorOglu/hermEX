package ews

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/mail"
	"net/textproto"
	"sort"
	"strings"
	"time"

	"hermex/internal/conversation"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// Conversation operations (MS-OXWSCONV) expose the thread-grouped view of a
// mailbox. The grouping reuses internal/conversation (the same id ActiveSync's
// conversation view emits), so a thread has one identity across protocols. The
// conversation id is not stored, so each request re-derives it by reading the
// candidate messages' headers; that scan is bounded by conversationScanCap to keep
// an unbounded mailbox from stalling a request.

// conversationScanCap bounds how many messages a single conversation request reads.
// A folder larger than this is scanned only up to the cap (the response notes the
// truncation is not surfaced to the client; the cap is a safety bound, not paging).
const conversationScanCap = 2000

// convMember is one message belonging to a conversation, with the sender display
// name and internet message id read from its headers.
type convMember struct {
	folderID          int64
	info              objectstore.MessageInfo
	sender            string
	internetMessageID string
}

// conversationGroups scans the given folders, derives each message's conversation
// id from its headers, and groups members by base64 conversation id. The scan
// stops at conversationScanCap.
func conversationGroups(st *objectstore.Store, folderIDs []int64) map[string][]convMember {
	groups := map[string][]convMember{}
	scanned := 0
	for _, fid := range folderIDs {
		infos, err := st.ListMessages(fid)
		if err != nil {
			continue
		}
		for _, info := range infos {
			if scanned >= conversationScanCap {
				return groups
			}
			scanned++
			raw, err := st.GetMessageRaw(fid, info.UID)
			if err != nil {
				continue
			}
			h := mime.ParseStructure(raw).Header()
			id := base64.StdEncoding.EncodeToString(conversation.ID(raw))
			groups[id] = append(groups[id], convMember{
				folderID:          fid,
				info:              info,
				sender:            headerFromDisplay(h),
				internetMessageID: strings.TrimSpace(h.Get("Message-Id")),
			})
		}
	}
	return groups
}

// allFolderIDs returns every client-visible folder id in the mailbox, the scan set
// for conversation operations that span folders.
func allFolderIDs(st *objectstore.Store) []int64 {
	folders, err := st.ListFolders()
	if err != nil {
		return nil
	}
	ids := make([]int64, 0, len(folders))
	for _, f := range folders {
		ids = append(ids, f.ID)
	}
	return ids
}

// headerFromDisplay extracts a message's From display name, falling back to the
// address.
func headerFromDisplay(h textproto.MIMEHeader) string {
	from := strings.TrimSpace(h.Get("From"))
	if addr, err := mail.ParseAddress(from); err == nil {
		if addr.Name != "" {
			return addr.Name
		}
		return addr.Address
	}
	return from
}

// --- FindConversation ---

type findConversationRequest struct {
	ParentFolderID folderRefs `xml:"ParentFolderId"`
}

type findConversationResponse struct {
	XMLName       xml.Name           `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindConversationResponse"`
	ResponseClass string             `xml:"ResponseClass,attr"`
	ResponseCode  string             `xml:"ResponseCode"`
	Conversations *conversationsWrap `xml:"Conversations,omitempty"`
}

type conversationsWrap struct {
	Items []conversationSummary `xml:"http://schemas.microsoft.com/exchange/services/2006/types Conversation"`
}

// conversationSummary is the load-bearing subset of ConversationType: the id, the
// topic, the distinct senders, the last delivery time, and the message and unread
// counts. The optional global/recipient/size/importance facets are omitted.
type conversationSummary struct {
	ConversationID    convIDElem `xml:"ConversationId"`
	ConversationTopic string     `xml:"ConversationTopic"`
	UniqueSenders     stringList `xml:"UniqueSenders"`
	LastDeliveryTime  string     `xml:"LastDeliveryTime"`
	MessageCount      int        `xml:"MessageCount"`
	UnreadCount       int        `xml:"UnreadCount"`
}

type convIDElem struct {
	ID string `xml:"Id,attr"`
}

type stringList struct {
	Strings []string `xml:"String"`
}

// handleFindConversation answers FindConversation: it groups the named folder's
// messages into conversations and returns a summary per conversation.
func (s *Server) handleFindConversation(w http.ResponseWriter, inner []byte, sess *session) {
	var req findConversationRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "FindConversation: "+err.Error())
		return
	}
	targets := resolveTargets(req.ParentFolderID)
	if len(targets) == 0 || !targets[0].ok || targets[0].mailbox != "" {
		writeResponse(w, findConversationResponse{ResponseClass: "Error", ResponseCode: "ErrorFolderNotFound"})
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeResponse(w, findConversationResponse{ResponseClass: "Error", ResponseCode: "ErrorInternalServerError"})
		return
	}
	defer st.Close()

	groups := conversationGroups(st, []int64{targets[0].fid})
	summaries := make([]conversationSummary, 0, len(groups))
	for id, members := range groups {
		summaries = append(summaries, summarizeConversation(id, members))
	}
	// Newest conversation first, the order a client expects for a mail view.
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].LastDeliveryTime > summaries[j].LastDeliveryTime
	})

	writeResponse(w, findConversationResponse{
		ResponseClass: "Success",
		ResponseCode:  "NoError",
		Conversations: &conversationsWrap{Items: summaries},
	})
}

// summarizeConversation builds one conversation's summary from its members.
func summarizeConversation(id string, members []convMember) conversationSummary {
	var topic string
	var last time.Time
	unread := 0
	seen := map[string]bool{}
	var senders []string
	for _, m := range members {
		if m.info.InternalDate.After(last) {
			last = m.info.InternalDate
			topic = m.info.Subject // the most recent subject is the conversation topic
		}
		if m.info.Flags&objectstore.FlagSeen == 0 {
			unread++
		}
		if m.sender != "" && !seen[m.sender] {
			seen[m.sender] = true
			senders = append(senders, m.sender)
		}
	}
	return conversationSummary{
		ConversationID:    convIDElem{ID: id},
		ConversationTopic: topic,
		UniqueSenders:     stringList{Strings: senders},
		LastDeliveryTime:  last.UTC().Format(time.RFC3339),
		MessageCount:      len(members),
		UnreadCount:       unread,
	}
}

// --- GetConversationItems ---

type convRequestItem struct {
	ConversationID convIDElem `xml:"ConversationId"`
}

type getConversationItemsRequest struct {
	FoldersToIgnore folderRefs `xml:"FoldersToIgnore"`
	Conversations   struct {
		Items []convRequestItem `xml:"Conversation"`
	} `xml:"Conversations"`
}

type getConversationItemsResponse struct {
	XMLName  xml.Name                              `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetConversationItemsResponse"`
	Messages []getConversationItemsResponseMessage `xml:"ResponseMessages>GetConversationItemsResponseMessage"`
}

type getConversationItemsResponseMessage struct {
	ResponseClass string                `xml:"ResponseClass,attr"`
	ResponseCode  string                `xml:"ResponseCode"`
	Conversation  *conversationResponse `xml:"http://schemas.microsoft.com/exchange/services/2006/types Conversation,omitempty"`
}

type conversationResponse struct {
	ConversationID    convIDElem    `xml:"ConversationId"`
	ConversationNodes convNodesWrap `xml:"ConversationNodes"`
}

type convNodesWrap struct {
	Nodes []convNode `xml:"ConversationNode"`
}

type convNode struct {
	InternetMessageID string        `xml:"InternetMessageId"`
	Items             convItemsWrap `xml:"Items"`
}

type convItemsWrap struct {
	Messages []convItem `xml:"Message"`
}

type convItem struct {
	ItemID           itemIDElem `xml:"ItemId"`
	Subject          string     `xml:"Subject"`
	DateTimeReceived string     `xml:"DateTimeReceived"`
}

type itemIDElem struct {
	ID string `xml:"Id,attr"`
}

// handleGetConversationItems answers GetConversationItems: for each requested
// conversation it returns the conversation's messages (one node per message),
// scanning every folder except those the client asked to ignore.
func (s *Server) handleGetConversationItems(w http.ResponseWriter, inner []byte, sess *session) {
	var req getConversationItemsRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetConversationItems: "+err.Error())
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	ignore := resolvedFolderSet(req.FoldersToIgnore)
	folderIDs := make([]int64, 0)
	for _, fid := range allFolderIDs(st) {
		if !ignore[fid] {
			folderIDs = append(folderIDs, fid)
		}
	}
	groups := conversationGroups(st, folderIDs)

	msgs := make([]getConversationItemsResponseMessage, 0, len(req.Conversations.Items))
	for _, c := range req.Conversations.Items {
		msgs = append(msgs, buildConversationItems(c.ConversationID.ID, groups[c.ConversationID.ID]))
	}
	writeResponse(w, getConversationItemsResponse{Messages: msgs})
}

// buildConversationItems renders one conversation's members oldest-first into
// conversation nodes.
func buildConversationItems(id string, members []convMember) getConversationItemsResponseMessage {
	ordered := append([]convMember(nil), members...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].info.InternalDate.Before(ordered[j].info.InternalDate)
	})
	nodes := make([]convNode, 0, len(ordered))
	for _, m := range ordered {
		nodes = append(nodes, convNode{
			InternetMessageID: m.internetMessageID,
			Items: convItemsWrap{Messages: []convItem{{
				ItemID:           itemIDElem{ID: oxews.EncodeItemID(oxews.ItemID{FolderID: m.folderID, MessageID: m.info.ID, UID: m.info.UID})},
				Subject:          m.info.Subject,
				DateTimeReceived: m.info.InternalDate.UTC().Format(time.RFC3339),
			}}},
		})
	}
	return getConversationItemsResponseMessage{
		ResponseClass: "Success",
		ResponseCode:  "NoError",
		Conversation: &conversationResponse{
			ConversationID:    convIDElem{ID: id},
			ConversationNodes: convNodesWrap{Nodes: nodes},
		},
	}
}

// resolvedFolderSet resolves a folder reference list to the set of folder ids it
// names in the caller's own mailbox.
func resolvedFolderSet(refs folderRefs) map[int64]bool {
	set := map[int64]bool{}
	for _, t := range resolveTargets(refs) {
		if t.ok && t.mailbox == "" {
			set[t.fid] = true
		}
	}
	return set
}

// --- ApplyConversationAction ---

type conversationAction struct {
	Action              string     `xml:"Action"`
	ConversationID      convIDElem `xml:"ConversationId"`
	DestinationFolderID folderRefs `xml:"DestinationFolderId"`
	IsRead              *bool      `xml:"IsRead"`
}

type applyConversationActionRequest struct {
	Actions []conversationAction `xml:"ConversationActions>ConversationAction"`
}

type applyConversationActionResponse struct {
	XMLName  xml.Name                `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ApplyConversationActionResponse"`
	Messages []folderResponseMessage `xml:"ResponseMessages>ApplyConversationActionResponseMessage"`
}

// handleApplyConversationAction answers ApplyConversationAction: a Move, Copy,
// Delete, or SetReadState applied to every message of each named conversation. The
// "Always*" variants apply the action now; the standing rule for future messages
// they also imply is not modelled in v1. A conversation spans folders, so the
// action sweeps every folder in the caller's mailbox.
func (s *Server) handleApplyConversationAction(w http.ResponseWriter, inner []byte, sess *session) {
	var req applyConversationActionRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "ApplyConversationAction: "+err.Error())
		return
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	defer st.Close()

	groups := conversationGroups(st, allFolderIDs(st))
	msgs := make([]folderResponseMessage, 0, len(req.Actions))
	for _, a := range req.Actions {
		msgs = append(msgs, applyOneConversationAction(st, a, groups[a.ConversationID.ID]))
	}
	writeResponse(w, applyConversationActionResponse{Messages: msgs})
}

// applyOneConversationAction applies a single action to a conversation's members.
func applyOneConversationAction(st *objectstore.Store, a conversationAction, members []convMember) folderResponseMessage {
	switch a.Action {
	case "Move", "AlwaysMove":
		target, ok := singleFolderTarget(a.DestinationFolderID)
		if !ok {
			return folderError("ErrorInvalidRequest")
		}
		for _, m := range members {
			_, _ = moveMessage(st, m.folderID, m.info.UID, target)
		}
	case "Copy":
		target, ok := singleFolderTarget(a.DestinationFolderID)
		if !ok {
			return folderError("ErrorInvalidRequest")
		}
		for _, m := range members {
			_, _ = copyMessage(st, m.folderID, m.info.UID, target)
		}
	case "Delete", "AlwaysDelete":
		for _, m := range members {
			_ = st.SoftDeleteMessage(m.folderID, m.info.UID)
		}
	case "SetReadState":
		read := a.IsRead != nil && *a.IsRead
		for _, m := range members {
			next := m.info.Flags
			if read {
				next |= objectstore.FlagSeen
			} else {
				next &^= objectstore.FlagSeen
			}
			if next != m.info.Flags {
				_ = st.SetMessageFlags(m.folderID, m.info.UID, next)
			}
		}
	default:
		return folderError("ErrorInvalidRequest")
	}
	return folderResponseMessage{ResponseClass: "Success", ResponseCode: "NoError"}
}

// singleFolderTarget resolves a destination folder reference to one folder id in
// the caller's own mailbox; ok is false for an unresolved or foreign target.
func singleFolderTarget(refs folderRefs) (int64, bool) {
	targets := resolveTargets(refs)
	if len(targets) == 0 || !targets[0].ok || targets[0].mailbox != "" {
		return 0, false
	}
	return targets[0].fid, true
}
