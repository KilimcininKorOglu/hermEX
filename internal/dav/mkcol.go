package dav

import (
	"net/http"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// handleMkCalendar creates a calendar collection (RFC 4791 §5.3.1). The URL's final
// segment names the collection and becomes the folder's display name, by which the
// collection is then addressed; an already-existing name fails 405. A request body
// (mkcalendar/set/prop) is accepted but its property directives are not applied,
// the display name is fixed to the URL segment so name-based resolution stays
// unambiguous (a documented v1 simplification).
func (s *Server) handleMkCalendar(w http.ResponseWriter, r *http.Request, mailbox string) {
	s.makeCollection(w, r, mailbox, true)
}

// handleMkCol creates an address-book or calendar collection via extended MKCOL
// (RFC 5689). The target home set (the URL prefix) decides the kind; the body's
// resourcetype is accepted but not required.
func (s *Server) handleMkCol(w http.ResponseWriter, r *http.Request, mailbox string) {
	s.makeCollection(w, r, mailbox, false)
}

// makeCollection is the shared create path for MKCALENDAR/MKCOL: it creates a child
// folder of the calendar or contacts root, typed so other protocols see it as a
// calendar/contacts folder rather than a mail folder. mkcalendar forces the calendar
// kind; MKCOL infers the kind from the URL home set.
func (s *Server) makeCollection(w http.ResponseWriter, r *http.Request, mailbox string, mkcalendar bool) {
	isCal, coll, failure := mkcolTarget(r, mkcalendar)
	if failure != "" {
		http.Error(w, failure, http.StatusForbidden)
		return
	}

	st, err := objectstore.Open(mailbox)
	if err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	defer st.Close()

	// MKCOL/MKCALENDAR on an existing resource (the reserved name or a prior
	// collection) fails (RFC 4918 §9.3.1, RFC 4791 §5.3.1).
	if _, exists, err := collectionByKind(st, isCal, coll); err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	} else if exists {
		w.Header().Set("Allow", allowMethods)
		http.Error(w, "collection already exists", http.StatusMethodNotAllowed)
		return
	}

	if err := createTypedCollection(st, isCal, coll); err != nil {
		s.davError(w, err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// mkcolTarget validates a create request's path and reports which home set it names
// plus the collection segment to create. A non-empty failure is the 403 message.
func mkcolTarget(r *http.Request, mkcalendar bool) (isCal bool, coll string, failure string) {
	kind, _, coll, _ := classify(r.URL.Path)
	isCal = strings.HasPrefix(r.URL.Path, "/dav/calendars/")
	isCard := strings.HasPrefix(r.URL.Path, "/dav/addressbooks/")
	if mkcalendar && !isCal {
		return false, "", "MKCALENDAR is only valid under the calendar home set"
	}
	if !creatableCollection(kind, coll, isCal, isCard) {
		return false, "", "not a collection path"
	}
	return isCal, coll, ""
}

// creatableCollection reports whether a path names a collection a client may create.
// A scheduling Inbox/Outbox path classifies as its own kind, not kindCalendar, so
// this also rejects MKCALENDAR/MKCOL on the reserved names (RFC 6638 §2.1/§2.2): a
// client cannot create a user calendar that shadows them.
func creatableCollection(kind resourceKind, coll string, isCal, isCard bool) bool {
	switch {
	case coll == "", !isCal && !isCard:
		return false
	case isCal:
		return kind == kindCalendar
	default:
		return kind == kindAddressbook
	}
}

// createTypedCollection creates the child folder and types it so other protocols see
// a calendar/contacts folder rather than a mail folder.
func createTypedCollection(st *objectstore.Store, isCal bool, coll string) error {
	parent, class := int64(mapi.PrivateFIDContacts), mapi.ContainerClassContact
	if isCal {
		parent, class = int64(mapi.PrivateFIDCalendar), mapi.ContainerClassAppointment
	}
	fid, err := st.CreateFolder(&parent, coll)
	if err != nil {
		return err
	}
	// CreateFolder defaults to the mail container class; retype the new folder.
	return st.SetFolderProperties(fid, mapi.PropertyValues{{Tag: mapi.PrContainerClass, Value: class}})
}
