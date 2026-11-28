package ews

import (
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
	"hermex/internal/oxews"
)

// createSmartResponses answers the ReplyToItem, ReplyAllToItem and ForwardItem
// elements. Each names the original by ReferenceItemId, which must be an item the
// caller may read; a missing one is refused with ErrorItemNotFound and nothing is
// sent. A response carrying NewBodyContent (new text only) gets the original added
// below it, and a forward its attachments, the way the EAS smart commands do; one
// carrying a whole Body is sent as the client wrote it.
func (s *Server) createSmartResponses(st *objectstore.Store, sess *session, req createItemRequest,
	disp string, send, save bool) []itemResponseMessage {
	cache := s.newStoreCache()
	defer cache.closeAll()
	var msgs []itemResponseMessage
	for _, group := range []struct {
		list    []smartResponse
		forward bool
	}{{req.Items.ReplyToItem, false}, {req.Items.ReplyAllToItem, false}, {req.Items.ForwardItem, true}} {
		for _, sr := range group.list {
			msgs = append(msgs, s.createSmartResponse(cache, st, sess, sr, group.forward, disp, send, save))
		}
	}
	return msgs
}

func (s *Server) createSmartResponse(cache *storeCache, st *objectstore.Store, sess *session, sr smartResponse,
	forward bool, disp string, send, save bool) itemResponseMessage {
	id, err := oxews.DecodeItemID(sr.ReferenceItemID.ID)
	if err != nil {
		return itemError("ErrorInvalidRequest")
	}
	src, code := cache.openForItem(sess, id, mapi.FrightsReadAny)
	if code != "" {
		return itemError(code)
	}
	ref, uid, ok := referencedMessage(src, id)
	if !ok {
		return itemError("ErrorItemNotFound")
	}
	orig, err := src.OpenMessage(ref)
	if err != nil {
		return itemError("ErrorItemNotFound")
	}
	m := sr.message()
	var complete func(*oxcmail.Message)
	if sr.NewBodyContent != nil {
		m.Body.Type, m.Body.Content = sr.NewBodyContent.Type, sr.NewBodyContent.Content
		complete = func(out *oxcmail.Message) { oxcmail.AppendOriginal(out, orig, forward) }
	}
	rm := s.createItemWith(st, sess, m, disp, send, save, complete)
	if send && rm.ResponseClass == "Success" {
		markResponded(src, id.FolderID, ref, uid, forward)
	}
	return rm
}

// referencedMessage finds the store id of the item a reference names: its message
// id, or for a mail item addressed by uid alone, the message at that uid.
func referencedMessage(st *objectstore.Store, id oxews.ItemID) (int64, uint32, bool) {
	if id.MessageID != 0 {
		return id.MessageID, id.UID, true
	}
	if id.UID == 0 {
		return 0, 0, false
	}
	info, err := st.MessageByUID(id.FolderID, id.UID)
	if err != nil {
		return 0, 0, false
	}
	return info.ID, id.UID, true
}

// markResponded stamps the reply or forward verb on the original ([MS-OXOMSG]
// PidTagLastVerbExecuted) and, for a reply to a mail item, the \Answered flag, so
// every client shows the icon. The response already left, so a failure is logged.
func markResponded(st *objectstore.Store, fid, ref int64, uid uint32, forward bool) {
	verb := mapi.NoteVerbReplyToSender
	if forward {
		verb = mapi.NoteVerbForward
	}
	var props mapi.PropertyValues
	props.Set(mapi.PrLastVerbExecuted, verb)
	props.Set(mapi.PrLastVerbExecutionTime, mapi.UnixToNTTime(time.Now()))
	if err := st.ModifyMessageProperties(ref, props); err != nil {
		st.LogSwallowedError("ews.mark_responded", err)
	}
	if forward || uid == 0 {
		return
	}
	if flags, err := st.MessageFlags(fid, uid); err == nil {
		if err := st.SetMessageFlags(fid, uid, flags|objectstore.FlagAnswered); err != nil {
			st.LogSwallowedError("ews.mark_responded_flags", err)
		}
	}
}
