package dav

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// CalDAV scheduling Inbox receiving (RFC 6638 §4.1). A pure-CalDAV client (Apple
// Calendar, Thunderbird) reads its scheduling Inbox collection to discover the
// invites the server delivered; a MAPI-family client renders the same meeting from
// the mail Inbox. To keep one stored object per invite (single-data), the scheduling
// Inbox is a VIEW over the incoming meeting-request mail in PrivateFIDInbox, never a
// second copy: members are the messages whose class is IPM.Schedule.Meeting.*, served
// as iCalendar, and a DELETE soft-deletes the backing message (the accept-then-delete
// flow files the accepted event into the calendar through a separate PUT).
//
// The filter reads each inbox message's class, so a very large mail Inbox makes the
// listing O(n); a class-indexed query is the future optimization.

// inboxItem is one scheduling Inbox member: the backing message id and its change
// number (for the ETag).
type inboxItem struct {
	id           int64
	changeNumber uint64
}

// name is the member's DAV resource name, synthesized from the message id because a
// delivered meeting-request mail carries no client-assigned resource name.
func (it inboxItem) name() string { return strconv.FormatInt(it.id, 10) + ".ics" }

// scheduleInboxItems returns the scheduling Inbox members: the IPM.Schedule.Meeting.*
// messages in the mail Inbox, in folder order.
func scheduleInboxItems(st *objectstore.Store) ([]inboxItem, error) {
	objs, err := st.ListFolderObjects(int64(mapi.PrivateFIDInbox))
	if err != nil {
		return nil, err
	}
	var items []inboxItem
	for _, o := range objs {
		if isScheduleMeeting(messageClass(st, o.ID)) {
			items = append(items, inboxItem{id: o.ID, changeNumber: o.ChangeNumber})
		}
	}
	return items, nil
}

// messageClass reads just a message's PR_MESSAGE_CLASS without loading the whole
// message (recipients, attachments, content files).
func messageClass(st *objectstore.Store, id int64) string {
	props, err := st.GetMessageProperties(id, mapi.PrMessageClass)
	if err != nil {
		return ""
	}
	if v, ok := props.Get(mapi.PrMessageClass); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// isScheduleMeeting reports whether a message class is an iTIP meeting object
// (request, cancellation, or response) that belongs in the scheduling Inbox.
func isScheduleMeeting(class string) bool {
	return strings.HasPrefix(class, "IPM.Schedule.Meeting")
}

// itipMethodForClass maps a meeting message class to the iTIP METHOD its served
// iCalendar must carry (RFC 5546): a request is REQUEST, a cancellation CANCEL, a
// response REPLY. Without a METHOD a client rejects the object as a non-scheduling
// resource.
func itipMethodForClass(class string) string {
	switch {
	case strings.HasPrefix(class, "IPM.Schedule.Meeting.Resp"):
		return "REPLY"
	case class == "IPM.Schedule.Meeting.Canceled":
		return "CANCEL"
	default:
		return "REQUEST"
	}
}

// findInboxItem returns the member whose resource name matches, scanning the current
// view so resolution and membership are checked together (the inbox URL space cannot
// reach a message that is not a delivered meeting request).
func findInboxItem(items []inboxItem, name string) (inboxItem, bool) {
	for _, it := range items {
		if it.name() == name {
			return it, true
		}
	}
	return inboxItem{}, false
}

// handleScheduleInboxGet serves a scheduling Inbox member as its iTIP iCalendar,
// re-exported from the stored meeting message with the METHOD its class implies.
func (s *Server) handleScheduleInboxGet(w http.ResponseWriter, r *http.Request, mailbox string) {
	_, _, _, name := classify(r.URL.Path)
	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	items, err := scheduleInboxItems(st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	it, ok := findInboxItem(items, name)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	msg, err := st.OpenMessage(it.id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ics, err := oxcical.Export(msg, icalOptions(st))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body := withMethod(string(ics), itipMethodForClass(messageStringProp(msg.Props, mapi.PrMessageClass)))
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("ETag", etag(it.changeNumber))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Write([]byte(body))
}

// handleScheduleInboxDelete removes a scheduling Inbox member, soft-deleting the
// backing meeting message (the dumpster path) so the change number tombstones it.
func (s *Server) handleScheduleInboxDelete(w http.ResponseWriter, r *http.Request, mailbox string) {
	_, _, _, name := classify(r.URL.Path)
	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	items, err := scheduleInboxItems(st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	it, ok := findInboxItem(items, name)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if im := r.Header.Get("If-Match"); im != "" && im != etag(it.changeNumber) {
		http.Error(w, "etag mismatch", http.StatusPreconditionFailed)
		return
	}
	if err := st.SoftDeleteObject(it.id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// messageStringProp returns a string-valued property of a message, or "".
func messageStringProp(p mapi.PropertyValues, tag mapi.PropTag) string {
	if v, ok := p.Get(tag); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
