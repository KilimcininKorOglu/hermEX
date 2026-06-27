package dav

import (
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// handlePropfind answers a PROPFIND. It walks the discovery chain a CardDAV
// client follows — root (current-user-principal) -> principal
// (addressbook-home-set) -> home set -> the Contacts address book — and, at
// Depth 1 on the address book, lists its member vCards. The requested property
// set is not filtered: a useful standard set is returned for each resource (a
// documented v1 simplification; clients ignore properties they did not ask for).
func (s *Server) handlePropfind(w http.ResponseWriter, r *http.Request, user, mailbox string) {
	kind, pathUser, coll, _ := classify(r.URL.Path)
	if pathUser == "" {
		pathUser = user
	}
	depth := r.Header.Get("Depth")

	var responses []msResponse
	switch kind {
	case kindRoot:
		responses = []msResponse{principalLink(r.URL.Path, pathUser)}
	case kindPrincipal:
		responses = []msResponse{principalResponse(pathUser)}
	case kindHomeSet:
		responses = []msResponse{homeSetResponse(pathUser)}
		if depth != "0" {
			// At the home set, Depth 1 lists the address books it contains (the
			// well-known Contacts plus any user-created ones), not their members.
			rs, err := s.allAddressbookCollections(mailbox, pathUser)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			responses = append(responses, rs...)
		}
	case kindAddressbook:
		rs, err := s.addressbookResponses(mailbox, pathUser, coll, depth)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rs == nil {
			http.Error(w, "no such address book", http.StatusNotFound)
			return
		}
		responses = rs
	case kindCalHomeSet:
		responses = []msResponse{calHomeSetResponse(pathUser)}
		if depth != "0" {
			// At the home set, Depth 1 lists the calendars it contains (the
			// well-known Calendar plus any user-created ones), not their members.
			rs, err := s.allCalendarCollections(mailbox, pathUser)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			responses = append(responses, rs...)
		}
	case kindCalendar:
		rs, err := s.calendarResponses(mailbox, pathUser, coll, depth)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rs == nil {
			http.Error(w, "no such calendar", http.StatusNotFound)
			return
		}
		responses = rs
	case kindScheduleInbox:
		rs, err := s.scheduleInboxResponses(mailbox, pathUser, depth)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		responses = rs
	case kindScheduleOutbox:
		responses = []msResponse{scheduleOutboxResponse(pathUser)}
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	writeMultistatus(w, &multistatus{Responses: responses})
}

// principalLink answers a root PROPFIND with the current-user-principal, the
// entry point of CardDAV discovery.
func principalLink(path, user string) msResponse {
	return msResponse{
		Href: path,
		Propstat: []msPropstat{{
			Prop:   msProp{CurrentUserPrOne: &href{Href: principalPath(user)}},
			Status: statusOK,
		}},
	}
}

// principalResponse describes the user principal: its URL, the home sets that lead
// to the address books and calendars, and the CalDAV scheduling discovery
// properties (calendar-user-address-set + the scheduling Inbox/Outbox URLs, RFC
// 6638 §2.2/§2.4.1) a client needs to drive auto-scheduling.
func principalResponse(user string) msResponse {
	return msResponse{
		Href: principalPath(user),
		Propstat: []msPropstat{{
			Prop: msProp{
				ResourceType:           &resourceType{Principal: empty},
				DisplayName:            user,
				PrincipalURL:           &href{Href: principalPath(user)},
				AddressbookHomeSet:     &href{Href: homeSetPath(user)},
				CalendarHomeSet:        &href{Href: calHomeSetPath(user)},
				CurrentUserPrOne:       &href{Href: principalPath(user)},
				CalendarUserAddressSet: &hrefSet{Hrefs: []string{"mailto:" + user, principalPath(user)}},
				ScheduleInboxURL:       &href{Href: scheduleInboxPath(user)},
				ScheduleOutboxURL:      &href{Href: scheduleOutboxPath(user)},
				Owner:                  &href{Href: principalPath(user)},
				CurrentUserPrivSet:     ownerPrivilegeSet(),
				SupportedReportSet:     principalReportSet(),
			},
			Status: statusOK,
		}},
	}
}

// homeSetResponse describes the addressbook-home-set collection.
func homeSetResponse(user string) msResponse {
	return msResponse{
		Href: homeSetPath(user),
		Propstat: []msPropstat{{
			Prop: msProp{
				ResourceType: &resourceType{Collection: empty},
				DisplayName:  "Address Books",
			},
			Status: statusOK,
		}},
	}
}

// cardCollectionResponse builds the collection-level PROPFIND response for one
// address book (no members).
func cardCollectionResponse(st *objectstore.Store, user, coll, displayName string, fid int64) (msResponse, error) {
	max, err := st.FolderObjectsSyncMax(fid)
	if err != nil {
		return msResponse{}, err
	}
	dead, err := st.ListDeadProps(fid)
	if err != nil {
		return msResponse{}, err
	}
	prop := msProp{
		ResourceType:       collectionResourceType(),
		DisplayName:        displayName,
		GetCTag:            ctag(max),
		SyncToken:          syncToken(max),
		Owner:              &href{Href: principalPath(user)},
		CurrentUserPrivSet: ownerPrivilegeSet(),
		SupportedReportSet: addressbookReportSet(),
	}
	prop.QuotaUsed, prop.QuotaAvailable = mailboxQuota(st)
	applyDeadProps(&prop, dead)
	return msResponse{
		Href:     addressbookPathColl(user, coll),
		Propstat: []msPropstat{{Prop: prop, Status: statusOK}},
	}, nil
}

// addressbookResponses returns one address book's collection response, followed
// (when depth != "0") by one response per member vCard. It resolves the named
// collection (the reserved "contacts" or a user-created one); a nil slice means no
// such collection exists.
func (s *Server) addressbookResponses(mailbox, user, coll, depth string) ([]msResponse, error) {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	fid, ok, err := cardCollectionFID(st, coll)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	displayName := "Contacts"
	if coll != "" && coll != addressbookName {
		displayName = coll
	}
	cr, err := cardCollectionResponse(st, user, coll, displayName, fid)
	if err != nil {
		return nil, err
	}
	responses := []msResponse{cr}
	if depth == "0" {
		return responses, nil
	}

	objs, err := st.ListFolderObjects(fid)
	if err != nil {
		return nil, err
	}
	for _, o := range objs {
		responses = append(responses, msResponse{
			Href: objectPathColl(user, coll, objectName(st, o.ID, ".vcf")),
			Propstat: []msPropstat{{
				Prop: msProp{
					GetETag:        etag(o.ChangeNumber),
					GetContentType: "text/vcard; charset=utf-8",
				},
				Status: statusOK,
			}},
		})
	}
	return responses, nil
}

// allAddressbookCollections lists every address book in the home set: the
// well-known Contacts plus each user-created child collection.
func (s *Server) allAddressbookCollections(mailbox, user string) ([]msResponse, error) {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	wk, err := cardCollectionResponse(st, user, addressbookName, "Contacts", int64(mapi.PrivateFIDContacts))
	if err != nil {
		return nil, err
	}
	out := []msResponse{wk}
	subs, err := childCollections(st, int64(mapi.PrivateFIDContacts))
	if err != nil {
		return nil, err
	}
	for _, f := range subs {
		cr, err := cardCollectionResponse(st, user, f.DisplayName, f.DisplayName, f.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	// Collection sharing (#117): each shared mailbox the caller is a delegate of also
	// appears in the caller's address-book home set, under the owner's principal href.
	shared, err := s.sharedAddressBooks(user)
	if err != nil {
		return nil, err
	}
	out = append(out, shared...)
	return out, nil
}

// calHomeSetResponse describes the calendar-home-set collection.
func calHomeSetResponse(user string) msResponse {
	return msResponse{
		Href: calHomeSetPath(user),
		Propstat: []msPropstat{{
			Prop: msProp{
				ResourceType: &resourceType{Collection: empty},
				DisplayName:  "Calendars",
			},
			Status: statusOK,
		}},
	}
}

// calCollectionResponse builds the collection-level PROPFIND response for one
// calendar (no members).
// componentSetFor reports the supported-calendar-component-set for a collection: the
// Tasks folder holds VTODOs, the Journal folder holds VJOURNALs, every other calendar
// holds VEVENTs.
func componentSetFor(fid int64) *supportedComp {
	switch fid {
	case int64(mapi.PrivateFIDTasks):
		return todoComponentSet()
	case int64(mapi.PrivateFIDJournal):
		return journalComponentSet()
	}
	return eventComponentSet()
}

func calCollectionResponse(st *objectstore.Store, user, coll, displayName string, fid int64) (msResponse, error) {
	max, err := st.FolderObjectsSyncMax(fid)
	if err != nil {
		return msResponse{}, err
	}
	dead, err := st.ListDeadProps(fid)
	if err != nil {
		return msResponse{}, err
	}
	prop := msProp{
		ResourceType:       calendarResourceType(),
		DisplayName:        displayName,
		GetCTag:            ctag(max),
		SyncToken:          syncToken(max),
		SupportedCalComp:   componentSetFor(fid),
		SupportedReportSet: calendarReportSet(),
		Owner:              &href{Href: principalPath(user)},
		CurrentUserPrivSet: ownerPrivilegeSet(),
	}
	prop.QuotaUsed, prop.QuotaAvailable = mailboxQuota(st)
	applyDeadProps(&prop, dead)
	return msResponse{
		Href:     calendarPathColl(user, coll),
		Propstat: []msPropstat{{Prop: prop, Status: statusOK}},
	}, nil
}

// calendarResponses returns one calendar's collection response, followed (when
// depth != "0") by one response per member .ics object. It resolves the named
// collection (the reserved "calendar" or a user-created one); a nil slice means no
// such collection exists.
func (s *Server) calendarResponses(mailbox, user, coll, depth string) ([]msResponse, error) {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	fid, ok, err := calCollectionFID(st, coll)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	displayName := "Calendar"
	if coll != "" && coll != calendarName {
		displayName = coll
	}
	cr, err := calCollectionResponse(st, user, coll, displayName, fid)
	if err != nil {
		return nil, err
	}
	responses := []msResponse{cr}
	if depth == "0" {
		return responses, nil
	}

	objs, err := st.ListFolderObjects(fid)
	if err != nil {
		return nil, err
	}
	for _, o := range objs {
		responses = append(responses, msResponse{
			Href: calObjectPathColl(user, coll, objectName(st, o.ID, ".ics")),
			Propstat: []msPropstat{{
				Prop: msProp{
					GetETag:        etag(o.ChangeNumber),
					GetContentType: "text/calendar; charset=utf-8",
				},
				Status: statusOK,
			}},
		})
	}
	return responses, nil
}

// allCalendarCollections lists every calendar in the home set: the well-known
// Calendar plus each user-created child collection.
func (s *Server) allCalendarCollections(mailbox, user string) ([]msResponse, error) {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	wk, err := calCollectionResponse(st, user, calendarName, "Calendar", int64(mapi.PrivateFIDCalendar))
	if err != nil {
		return nil, err
	}
	out := []msResponse{wk}
	// The Tasks folder is advertised as its own calendar collection (VTODO) alongside
	// the event calendar, so a CalDAV tasks client discovers it in the home set.
	tasks, err := calCollectionResponse(st, user, tasksName, "Tasks", int64(mapi.PrivateFIDTasks))
	if err != nil {
		return nil, err
	}
	out = append(out, tasks)
	// The Journal folder is advertised as its own calendar collection (VJOURNAL) too, so
	// a CalDAV journal client discovers it in the home set.
	journal, err := calCollectionResponse(st, user, journalName, "Journal", int64(mapi.PrivateFIDJournal))
	if err != nil {
		return nil, err
	}
	out = append(out, journal)
	subs, err := childCollections(st, int64(mapi.PrivateFIDCalendar))
	if err != nil {
		return nil, err
	}
	for _, f := range subs {
		cr, err := calCollectionResponse(st, user, f.DisplayName, f.DisplayName, f.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	// Collection sharing (#117): each shared mailbox the caller is a delegate of also
	// appears in the caller's calendar home set, under the owner's principal href, so a
	// client discovers it without configuring the owner's URL by hand. Access is gated
	// at the routing layer (resolveTarget).
	shared, err := s.sharedCalendars(user)
	if err != nil {
		return nil, err
	}
	out = append(out, shared...)
	// The scheduling Inbox and Outbox live within the calendar home collection
	// (RFC 6638 §2.1.1); list them alongside the calendars so a client browsing the
	// home set discovers them.
	in, err := scheduleInboxCollectionResponse(st, user)
	if err != nil {
		return nil, err
	}
	out = append(out, in, scheduleOutboxResponse(user))
	return out, nil
}

// scheduleInboxCollectionResponse builds the collection-level PROPFIND response for
// the scheduling Inbox (no members). The backing folder is created lazily on first
// delivery, so an Inbox that has received nothing reports an empty collection.
func scheduleInboxCollectionResponse(st *objectstore.Store, user string) (msResponse, error) {
	// The scheduling Inbox is a view over the meeting-request mail in the Inbox, so its
	// ctag tracks that folder's change high-water mark. Any inbox write moves it, which
	// over-signals (a client re-reads and finds nothing new) but never goes stale on a
	// delivered invite.
	max, err := st.FolderObjectsSyncMax(int64(mapi.PrivateFIDInbox))
	if err != nil {
		return msResponse{}, err
	}
	return msResponse{
		Href: scheduleInboxPath(user),
		Propstat: []msPropstat{{
			Prop: msProp{
				ResourceType:       scheduleInboxResourceType(),
				DisplayName:        "Inbox",
				GetCTag:            ctag(max),
				SyncToken:          syncToken(max),
				SupportedCalComp:   eventComponentSet(),
				SupportedReportSet: calendarReportSet(),
				Owner:              &href{Href: principalPath(user)},
				CurrentUserPrivSet: ownerPrivilegeSet(),
			},
			Status: statusOK,
		}},
	}, nil
}

// scheduleInboxResponses returns the scheduling Inbox collection response, followed
// (when depth != "0") by one response per delivered scheduling message.
func (s *Server) scheduleInboxResponses(mailbox, user, depth string) ([]msResponse, error) {
	st, err := objectstore.Open(mailbox)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	cr, err := scheduleInboxCollectionResponse(st, user)
	if err != nil {
		return nil, err
	}
	responses := []msResponse{cr}
	if depth == "0" {
		return responses, nil
	}

	items, err := scheduleInboxItems(st)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		responses = append(responses, msResponse{
			Href: scheduleInboxPath(user) + it.name(),
			Propstat: []msPropstat{{
				Prop: msProp{
					GetETag:        etag(it.changeNumber),
					GetContentType: "text/calendar; charset=utf-8",
				},
				Status: statusOK,
			}},
		})
	}
	return responses, nil
}

// scheduleOutboxResponse builds the collection-level PROPFIND response for the
// scheduling Outbox. The Outbox stores nothing (it is POST-only, RFC 6638 §2.2), so
// it is a purely synthetic collection.
func scheduleOutboxResponse(user string) msResponse {
	return msResponse{
		Href: scheduleOutboxPath(user),
		Propstat: []msPropstat{{
			Prop: msProp{
				ResourceType:       scheduleOutboxResourceType(),
				DisplayName:        "Outbox",
				SupportedCalComp:   eventComponentSet(),
				SupportedReportSet: calendarReportSet(),
				Owner:              &href{Href: principalPath(user)},
				CurrentUserPrivSet: ownerPrivilegeSet(),
			},
			Status: statusOK,
		}},
	}
}

// writeMultistatus serializes and writes a 207 Multistatus response.
func writeMultistatus(w http.ResponseWriter, ms *multistatus) {
	body, err := marshalMultistatus(ms)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", `application/xml; charset=utf-8`)
	w.WriteHeader(http.StatusMultiStatus)
	w.Write(body)
}

// etag is a quoted entity tag derived from an object's change number.
func etag(cn uint64) string { return `"` + strconv.FormatUint(cn, 10) + `"` }

// scheduleTag derives a CALDAV:schedule-tag from an object's change number (RFC 6638
// 3.2.10). Its format is distinct from etag so a client treats the two independently.
// It changes on every direct PUT/COPY/MOVE (rule 3); hermEX has no server-side
// partstat-only update path (rule 2), so today it tracks the ETag in lockstep.
func scheduleTag(cn uint64) string { return `"ST` + strconv.FormatUint(cn, 10) + `"` }

// ctag is a collection tag: the highest member change number, opaque to clients.
func ctag(max uint64) string { return strconv.FormatUint(max, 10) }

// syncToken is an opaque RFC 6578 sync token carrying the collection's change
// high-water mark.
func syncToken(max uint64) string { return "hermex:sync:" + strconv.FormatUint(max, 10) }

// mailboxQuota reports the mailbox's used bytes and, when a storage limit is set,
// the bytes still available (RFC 4331). Both are empty (and so omitted) when the
// figures cannot be read; available is also empty for an unlimited mailbox. The
// figure is mailbox-wide, reported the same on every collection.
func mailboxQuota(st *objectstore.Store) (used, available string) {
	n, err := st.MailboxSize()
	if err != nil {
		return "", ""
	}
	used = strconv.FormatInt(n, 10)
	if q, err := st.GetQuota(); err == nil && q.StorageKB > 0 {
		available = strconv.FormatInt(max(int64(q.StorageKB)*1024-n, 0), 10)
	}
	return used, available
}
