package dav

import (
	"encoding/xml"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxvcard"
)

// reportReq captures the fields of the CardDAV REPORTs we support. The root
// element name selects the report; href children drive addressbook-multiget and
// sync-token drives sync-collection.
type reportReq struct {
	XMLName   xml.Name
	Hrefs     []string `xml:"href"`
	SyncToken string   `xml:"sync-token"`
	Filter    *filter  `xml:"filter"`
}

// handleReport dispatches a CardDAV REPORT (RFC 6352 §8) on the addressbook
// collection: addressbook-multiget, addressbook-query, and sync-collection
// (RFC 6578). Each returns 207 Multistatus with the requested vCards.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request, mailbox string) {
	kind, user, _ := classify(r.URL.Path)
	if user == "" {
		http.Error(w, "not a collection", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, s.vcardLimit()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req reportReq
	if err := xml.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid report body: "+err.Error(), http.StatusBadRequest)
		return
	}

	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	switch req.XMLName.Local {
	case "addressbook-multiget":
		s.reportMultiget(w, st, req.Hrefs)
	case "addressbook-query":
		s.reportQueryOrSync(w, st, user, 0, false)
	case "calendar-multiget":
		s.reportCalMultiget(w, st, req.Hrefs)
	case "calendar-query":
		s.reportCalQueryOrSync(w, st, user, 0, false, req.Filter)
	case "free-busy-query":
		// Free/busy aggregation is not implemented in v1; return an empty result.
		writeMultistatus(w, &multistatus{})
	case "sync-collection":
		if kind == kindCalendar {
			s.reportCalQueryOrSync(w, st, user, parseSyncToken(req.SyncToken), true, nil)
		} else {
			s.reportQueryOrSync(w, st, user, parseSyncToken(req.SyncToken), true)
		}
	default:
		http.Error(w, "unsupported report", http.StatusForbidden)
	}
}

// reportMultiget returns address-data for each requested href, with a 404 status
// for any href that no longer resolves.
func (s *Server) reportMultiget(w http.ResponseWriter, st *objectstore.Store, hrefs []string) {
	ms := &multistatus{}
	for _, h := range hrefs {
		name := path.Base(strings.TrimRight(h, "/"))
		obj, found, err := findObjectByName(st, mapi.PrivateFIDContacts, ".vcf", name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !found {
			ms.Responses = append(ms.Responses, msResponse{Href: h, Status: statusNotFound})
			continue
		}
		data, err := addressData(st, obj.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ms.Responses = append(ms.Responses, addressDataResponse(h, obj.ChangeNumber, data))
	}
	writeMultistatus(w, ms)
}

// reportQueryOrSync returns address-data for the collection's members. For
// sync-collection only members whose change number exceeds the client's token
// are returned, and the response carries a fresh sync-token. addressbook-query
// filtering is not yet applied: every member is returned (a documented v1
// simplification). Deletions are not reported incrementally — the store
// hard-deletes without a tombstone — so a client reconciles removals on its own.
func (s *Server) reportQueryOrSync(w http.ResponseWriter, st *objectstore.Store, user string, sinceToken uint64, sync bool) {
	objs, err := st.ListFolderObjects(mapi.PrivateFIDContacts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	max, err := st.FolderMaxChangeNumber(mapi.PrivateFIDContacts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ms := &multistatus{}
	for _, o := range objs {
		if sync && o.ChangeNumber <= sinceToken {
			continue
		}
		data, err := addressData(st, o.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		href := objectPath(user, objectName(st, o.ID, ".vcf"))
		ms.Responses = append(ms.Responses, addressDataResponse(href, o.ChangeNumber, data))
	}
	if sync {
		ms.SyncToken = syncToken(max)
	}
	writeMultistatus(w, ms)
}

// addressData exports a stored contact to its vCard text.
func addressData(st *objectstore.Store, id int64) (string, error) {
	msg, err := st.OpenMessage(id)
	if err != nil {
		return "", err
	}
	vcf, err := oxvcard.Export(msg, vcardOptions(st))
	if err != nil {
		return "", err
	}
	return string(vcf), nil
}

// addressDataResponse builds a 200 response carrying a member's ETag and vCard.
func addressDataResponse(href string, cn uint64, data string) msResponse {
	return msResponse{
		Href: href,
		Propstat: []msPropstat{{
			Prop:   msProp{GetETag: etag(cn), AddressData: data},
			Status: statusOK,
		}},
	}
}

// reportCalMultiget returns calendar-data for each requested href, mirroring
// reportMultiget for the Calendar folder.
func (s *Server) reportCalMultiget(w http.ResponseWriter, st *objectstore.Store, hrefs []string) {
	ms := &multistatus{}
	for _, h := range hrefs {
		name := path.Base(strings.TrimRight(h, "/"))
		obj, found, err := findObjectByName(st, mapi.PrivateFIDCalendar, ".ics", name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !found {
			ms.Responses = append(ms.Responses, msResponse{Href: h, Status: statusNotFound})
			continue
		}
		data, err := calendarData(st, obj.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ms.Responses = append(ms.Responses, calendarDataResponse(h, obj.ChangeNumber, data))
	}
	writeMultistatus(w, ms)
}

// reportCalQueryOrSync returns calendar-data for the Calendar folder's members,
// mirroring reportQueryOrSync. calendar-query filtering is not applied: every
// member is returned (a documented v1 simplification, and a heavier one than for
// contacts because a calendar grows unbounded over time and the client re-pulls
// it each query). Deletions are not reported incrementally — the store
// hard-deletes without a tombstone.
func (s *Server) reportCalQueryOrSync(w http.ResponseWriter, st *objectstore.Store, user string, sinceToken uint64, sync bool, filt *filter) {
	objs, err := st.ListFolderObjects(mapi.PrivateFIDCalendar)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	max, err := st.FolderMaxChangeNumber(mapi.PrivateFIDCalendar)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ms := &multistatus{}
	for _, o := range objs {
		if sync && o.ChangeNumber <= sinceToken {
			continue
		}
		data, err := calendarData(st, o.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// calendar-query: skip objects that do not satisfy the filter (RFC 4791 §9.7).
		if !calendarMatches(filt, data) {
			continue
		}
		href := calObjectPath(user, objectName(st, o.ID, ".ics"))
		ms.Responses = append(ms.Responses, calendarDataResponse(href, o.ChangeNumber, data))
	}
	if sync {
		ms.SyncToken = syncToken(max)
	}
	writeMultistatus(w, ms)
}

// calendarData exports a stored appointment to its iCalendar text.
func calendarData(st *objectstore.Store, id int64) (string, error) {
	msg, err := st.OpenMessage(id)
	if err != nil {
		return "", err
	}
	ics, err := oxcical.Export(msg, icalOptions(st))
	if err != nil {
		return "", err
	}
	return string(ics), nil
}

// calendarDataResponse builds a 200 response carrying a member's ETag and iCalendar.
func calendarDataResponse(href string, cn uint64, data string) msResponse {
	return msResponse{
		Href: href,
		Propstat: []msPropstat{{
			Prop:   msProp{GetETag: etag(cn), CalendarData: data},
			Status: statusOK,
		}},
	}
}

// parseSyncToken extracts the change-number high-water mark from a sync token.
// An empty or unrecognized token means an initial sync (everything is new).
func parseSyncToken(token string) uint64 {
	const prefix = "hermex:sync:"
	if !strings.HasPrefix(token, prefix) {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(token, prefix), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
