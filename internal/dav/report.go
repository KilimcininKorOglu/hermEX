package dav

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxvcard"
)

// reportReq captures the fields of the CardDAV REPORTs we support. The root
// element name selects the report; href children drive addressbook-multiget and
// sync-token drives sync-collection.
type reportReq struct {
	XMLName   xml.Name
	Hrefs     []string   `xml:"href"`
	SyncToken string     `xml:"sync-token"`
	Filter    *filter    `xml:"filter"`
	TimeRange *timeRange `xml:"time-range"` // free-busy-query's direct time-range child
}

// handleReport dispatches a CardDAV REPORT (RFC 6352 §8) on the addressbook
// collection: addressbook-multiget, addressbook-query, and sync-collection
// (RFC 6578). Each returns 207 Multistatus with the requested vCards.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request, mailbox string) {
	_, user, coll, _ := classify(r.URL.Path)
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

	// Principal-search reports query the directory, not a mailbox store, so they are
	// answered before any collection is resolved (RFC 3744 §9.3/§9.5).
	switch req.XMLName.Local {
	case "principal-property-search":
		s.reportPrincipalSearch(w, body)
		return
	case "principal-search-property-set":
		reportPrincipalSearchPropSet(w)
		return
	case "expand-property":
		s.reportExpandProperty(w, r, user, body)
		return
	}

	if user == "" {
		http.Error(w, "not a collection", http.StatusBadRequest)
		return
	}
	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	// Resolve the target collection from the URL: a calendar path addresses a
	// calendar folder, an address-book path a contacts folder. The named collection
	// ("calendar"/"contacts" or a user-created one) is the folder all members live in.
	isCal := strings.HasPrefix(r.URL.Path, "/dav/calendars/")
	var fid int64
	var ok bool
	if isCal {
		fid, ok, err = calCollectionFID(st, coll)
	} else {
		fid, ok, err = cardCollectionFID(st, coll)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such collection", http.StatusNotFound)
		return
	}

	switch req.XMLName.Local {
	case "addressbook-multiget":
		s.reportMultiget(w, st, fid, req.Hrefs)
	case "addressbook-query":
		s.reportQueryOrSync(w, st, user, coll, fid, 0, false, req.Filter)
	case "calendar-multiget":
		s.reportCalMultiget(w, st, fid, req.Hrefs)
	case "calendar-query":
		s.reportCalQueryOrSync(w, st, user, coll, fid, 0, false, req.Filter)
	case "free-busy-query":
		s.handleFreeBusy(w, st, fid, req.TimeRange)
	case "sync-collection":
		if isCal {
			s.reportCalQueryOrSync(w, st, user, coll, fid, parseSyncToken(req.SyncToken), true, nil)
		} else {
			s.reportQueryOrSync(w, st, user, coll, fid, parseSyncToken(req.SyncToken), true, nil)
		}
	default:
		http.Error(w, "unsupported report", http.StatusForbidden)
	}
}

// reportMultiget returns address-data for each requested href, with a 404 status
// for any href that no longer resolves.
func (s *Server) reportMultiget(w http.ResponseWriter, st *objectstore.Store, fid int64, hrefs []string) {
	ms := &multistatus{}
	for _, h := range hrefs {
		name := path.Base(strings.TrimRight(h, "/"))
		obj, found, err := findObjectByName(st, fid, ".vcf", name)
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
func (s *Server) reportQueryOrSync(w http.ResponseWriter, st *objectstore.Store, user, coll string, fid int64, sinceToken uint64, sync bool, filt *filter) {
	objs, err := st.ListFolderObjects(fid)
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
		// addressbook-query: skip vCards that do not satisfy the filter (RFC 6352 §10.5).
		if !vcardMatches(filt, data) {
			continue
		}
		href := objectPathColl(user, coll, objectName(st, o.ID, ".vcf"))
		ms.Responses = append(ms.Responses, addressDataResponse(href, o.ChangeNumber, data))
	}
	if sync {
		// Tombstones: report each contact removed since the client's token as a 404
		// member so it deletes the vCard locally (RFC 6578).
		deleted, err := st.DeletedObjectsSince(fid, sinceToken)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, d := range deleted {
			href := objectPathColl(user, coll, objectName(st, d.ID, ".vcf"))
			ms.Responses = append(ms.Responses, msResponse{Href: href, Status: statusNotFound})
		}
		syncMax, err := st.FolderObjectsSyncMax(fid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ms.SyncToken = syncToken(syncMax)
	}
	writeMultistatus(w, ms)
}

// handleFreeBusy answers a CALDAV:free-busy-query (RFC 4791 §7.10): it aggregates
// the busy (non-transparent) VEVENTs overlapping the requested range into a
// VFREEBUSY component, returned as text/calendar.
func (s *Server) handleFreeBusy(w http.ResponseWriter, st *objectstore.Store, fid int64, tr *timeRange) {
	var rangeStart, rangeEnd time.Time
	var okS, okE bool
	if tr != nil {
		rangeStart, okS = parseFilterTime(tr.Start)
		rangeEnd, okE = parseFilterTime(tr.End)
	}
	periods, err := busyPeriods(st, fid, rangeStart, rangeEnd, okS, okE)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//hermEX//CalDAV//EN\r\nBEGIN:VFREEBUSY\r\n")
	if okS {
		fmt.Fprintf(&b, "DTSTART:%s\r\n", formatICalUTCZ(rangeStart))
	}
	if okE {
		fmt.Fprintf(&b, "DTEND:%s\r\n", formatICalUTCZ(rangeEnd))
	}
	fmt.Fprintf(&b, "DTSTAMP:%s\r\n", formatICalUTCZ(time.Now()))
	for _, p := range periods {
		fmt.Fprintf(&b, "FREEBUSY;FBTYPE=BUSY:%s\r\n", p)
	}
	b.WriteString("END:VFREEBUSY\r\nEND:VCALENDAR\r\n")
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
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
func (s *Server) reportCalMultiget(w http.ResponseWriter, st *objectstore.Store, fid int64, hrefs []string) {
	ms := &multistatus{}
	for _, h := range hrefs {
		name := path.Base(strings.TrimRight(h, "/"))
		obj, found, err := findObjectByName(st, fid, ".ics", name)
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
func (s *Server) reportCalQueryOrSync(w http.ResponseWriter, st *objectstore.Store, user, coll string, fid int64, sinceToken uint64, sync bool, filt *filter) {
	objs, err := st.ListFolderObjects(fid)
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
		href := calObjectPathColl(user, coll, objectName(st, o.ID, ".ics"))
		ms.Responses = append(ms.Responses, calendarDataResponse(href, o.ChangeNumber, data))
	}
	if sync {
		// Tombstones: report each event removed since the client's token as a 404
		// member so it deletes the .ics locally (RFC 6578).
		deleted, err := st.DeletedObjectsSince(fid, sinceToken)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, d := range deleted {
			href := calObjectPathColl(user, coll, objectName(st, d.ID, ".ics"))
			ms.Responses = append(ms.Responses, msResponse{Href: href, Status: statusNotFound})
		}
		syncMax, err := st.FolderObjectsSyncMax(fid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ms.SyncToken = syncToken(syncMax)
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
