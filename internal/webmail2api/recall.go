package webmail2api

import (
	"net/http"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

type recallResult struct {
	Recipient string `json:"recipient"`
	Status    string `json:"status"` // recalled | read | unavailable
}

// handleRecall retracts a message the caller sent. For each local recipient the
// caller's copy is deleted from their inbox if still unread (status "recalled"),
// left untouched if already read ("read"); external or non-local recipients are
// reported "unavailable". Only the message's own author may recall it: the id must
// resolve to a message in the caller's own mailbox whose sender is the caller, so a
// recipient cannot recall a message addressed to them (403).
func (s *Server) handleRecall(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	st, fid, uid, ok := s.locate(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	defer st.Close()
	m, err := st.MessageByUID(fid, uid)
	if err != nil {
		// Not a message in the caller's mailbox: they did not send it.
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you can only recall messages you sent"})
		return
	}
	msg, err := st.OpenMessage(m.ID)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you can only recall messages you sent"})
		return
	}
	// Authorization: the message's author must be the caller, so a recipient holding
	// their own copy of the message still cannot recall it.
	sender := senderOf(msg.Props)
	if sender == "" || !strings.EqualFold(sender, c.Email) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you can only recall messages you sent"})
		return
	}
	messageID := propStr(msg.Props, mapi.PrInternetMessageID)
	recips := recipientAddrs(msg.Recipients)
	results := make([]recallResult, 0, len(recips))
	recalled := 0
	if messageID != "" {
		for _, addr := range recips {
			status := s.recallFromRecipient(addr, messageID, sender)
			if status == "recalled" {
				recalled++
			}
			results = append(results, recallResult{Recipient: addr, Status: status})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"recalled": recalled, "total": len(recips), "results": results})
}

// recallFromRecipient removes a local recipient's unread copy of the message and
// reports the outcome. The copy is matched by internet message id in the recipient's
// inbox AND confirmed to carry the original sender before deletion, so a recall can
// never delete an unrelated message that happens to share a message id.
func (s *Server) recallFromRecipient(addr, messageID, sender string) string {
	path, ok := s.accounts.Resolve(addr)
	if !ok {
		return "unavailable" // external or unknown recipient: out of our reach
	}
	rst, err := objectstore.Open(path)
	if err != nil {
		return "unavailable"
	}
	defer rst.Close()
	objs, err := rst.ListFolderObjects(mapi.PrivateFIDInbox)
	if err != nil {
		return "unavailable"
	}
	for _, o := range objs {
		props, err := rst.GetMessageProperties(o.ID, mapi.PrInternetMessageID,
			mapi.PrSentRepresentingSmtpAddress, mapi.PrSenderSmtpAddress,
			mapi.PrSentRepresentingEmailAddress, mapi.PrSenderEmailAddress)
		if err != nil {
			continue
		}
		if !strings.EqualFold(propStr(props, mapi.PrInternetMessageID), messageID) {
			continue
		}
		// Defence in depth: only the original author's copy is eligible.
		copySender := senderOf(props)
		if !strings.EqualFold(copySender, sender) {
			continue
		}
		read, err := rst.GetMessageReadState(o.ID)
		if err != nil {
			return "unavailable"
		}
		if read {
			return "read" // already seen: too late to recall
		}
		if err := rst.DeleteObject(o.ID); err != nil {
			return "unavailable"
		}
		return "recalled"
	}
	return "unavailable" // no matching unread copy in the inbox (read, moved, or never local)
}

// recipientAddrs returns the distinct SMTP recipient addresses of a message.
func recipientAddrs(recips []mapi.PropertyValues) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range recips {
		addr := firstNonEmpty(r, mapi.PrSmtpAddress, mapi.PrEmailAddress)
		if addr == "" {
			continue
		}
		key := strings.ToLower(addr)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, addr)
	}
	return out
}

// senderOf returns a message's author address, preferring the sent-representing
// identity (the From header per MS-OXCMAIL) over the sender, in SMTP then legacy form.
func senderOf(pv mapi.PropertyValues) string {
	return firstNonEmpty(pv,
		mapi.PrSentRepresentingSmtpAddress, mapi.PrSenderSmtpAddress,
		mapi.PrSentRepresentingEmailAddress, mapi.PrSenderEmailAddress)
}

// firstNonEmpty returns the first non-empty string property among the given tags.
func firstNonEmpty(pv mapi.PropertyValues, tags ...mapi.PropTag) string {
	for _, tag := range tags {
		if v := propStr(pv, tag); v != "" {
			return v
		}
	}
	return ""
}
