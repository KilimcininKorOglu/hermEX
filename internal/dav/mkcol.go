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
// (mkcalendar/set/prop) is accepted but its property directives are not applied —
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
	kind, _, coll, _ := classify(r.URL.Path)
	isCal := strings.HasPrefix(r.URL.Path, "/dav/calendars/")
	isCard := strings.HasPrefix(r.URL.Path, "/dav/addressbooks/")
	if mkcalendar && !isCal {
		http.Error(w, "MKCALENDAR is only valid under the calendar home set", http.StatusForbidden)
		return
	}
	if coll == "" || (isCal && kind != kindCalendar) || (isCard && kind != kindAddressbook) || (!isCal && !isCard) {
		http.Error(w, "not a collection path", http.StatusForbidden)
		return
	}

	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	var parent int64
	var class string
	var resolve func(*objectstore.Store, string) (int64, bool, error)
	if isCal {
		parent, class, resolve = int64(mapi.PrivateFIDCalendar), mapi.ContainerClassAppointment, calCollectionFID
	} else {
		parent, class, resolve = int64(mapi.PrivateFIDContacts), mapi.ContainerClassContact, cardCollectionFID
	}

	// MKCOL/MKCALENDAR on an existing resource (the reserved name or a prior
	// collection) fails (RFC 4918 §9.3.1, RFC 4791 §5.3.1).
	if _, ok, err := resolve(st, coll); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if ok {
		w.Header().Set("Allow", allowMethods)
		http.Error(w, "collection already exists", http.StatusMethodNotAllowed)
		return
	}

	fid, err := st.CreateFolder(&parent, coll)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// CreateFolder defaults to the mail container class; retype the new folder so
	// the rest of the system treats it as a calendar/contacts collection.
	if err := st.SetFolderProperties(fid, mapi.PropertyValues{{Tag: mapi.PrContainerClass, Value: class}}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
