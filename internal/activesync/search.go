package activesync

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// Search Status values (MS-ASCMD 2.2.3.166.3): the overall command status and the
// per-store status carried in the response.
const (
	srStatusOK          = 1 // the command succeeded
	srStatusServerError = 3 // the request carried no store to search
	srStoreOK           = 1 // the store search succeeded
	srStoreReqInvalid   = 2 // the store/query is not supported here
	srStoreServerError  = 3 // the store could not be searched
)

// logSearchFail records a store search the server could not run.
func (s *Server) logSearchFail(sess *session, err error) {
	s.logDirectoryFail("search.gal.fail", sess, err)
}

// logResolveFail records a recipient resolution the server could not run.
func (s *Server) logResolveFail(sess *session, err error) {
	s.logDirectoryFail("resolve.gal.fail", sess, err)
}

// logDirectoryFail records an address-book read that failed.
func (s *Server) logDirectoryFail(name string, sess *session, err error) {
	s.Logger.Emit(logging.Event{
		Level:      logging.LevelError,
		Subsystem:  logging.ActiveSync,
		Name:       name,
		User:       sess.user,
		RemoteAddr: sess.tel.IP,
		Fields:     logging.Fields{"error": err.Error()},
	})
}

// handleSearch answers Search ([MS-ASCMD] 2.2.2.16). The GAL store resolves the
// Query against the directory address book; the Mailbox store scans the caller's
// own mail folders for messages matching the query's FreeText. A DocumentLibrary
// search reports a request-invalid store status (hermEX has no document store)
// rather than silently claiming an empty result.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		s.failRequest(w, r, "wbxml.parse.fail", err, http.StatusBadRequest, "invalid WBXML")
		return
	}
	store := root.Child(wbxml.SRStore)
	if store == nil {
		writeWBXML(w, searchStatus(srStatusServerError))
		return
	}
	name := store.ChildText(wbxml.SRName)

	switch {
	case strings.EqualFold(name, "GAL"):
		s.handleGALSearch(w, store, sess)
	case strings.EqualFold(name, "Mailbox"):
		s.handleMailboxSearch(w, store, sess)
	default:
		writeWBXML(w, searchGALReply(srStoreReqInvalid, nil, 0, 0))
	}
}

// handleMailboxSearch scans the caller's OWN mailbox (the authenticated session,
// never a client-supplied identity; OWASP A01) for messages whose subject, sender,
// or body match the query's FreeText. Matches are sorted newest-first and the
// requested Range is sliced out (the handler is stateless, so the sort makes paging
// deterministic across requests). Each hit renders the listing properties + the body
// in the requested representation, the same render the Sync command uses, so the
// device can show and open a result without a second fetch.
func (s *Server) handleMailboxSearch(w http.ResponseWriter, store *wbxml.Node, sess *session) {
	freetext := strings.ToLower(strings.TrimSpace(descendantText(store.Child(wbxml.SRQuery), wbxml.SRFreeText)))
	folderFilter := descendantText(store.Child(wbxml.SRQuery), wbxml.ASCollectionID)

	pref := bodyPref{}
	if opts := store.Child(wbxml.SROptions); opts != nil {
		pref = parseBodyPref(opts)
	}
	lo, hi, status := searchRange(store)
	if status != srStoreOK {
		writeWBXML(w, mailboxSearchReply(status, nil, 0, 0, 0))
		return
	}

	if freetext == "" {
		writeWBXML(w, mailboxSearchReply(srStoreOK, nil, 0, 0, 0))
		return
	}

	st, err := objectstore.OpenExisting(sess.mailbox)
	if err != nil {
		writeWBXML(w, mailboxSearchReply(srStatusServerError, nil, 0, 0, 0))
		return
	}
	defer st.Close()

	hits := mailboxSearchHits(st, freetext, folderFilter)

	total := len(hits)
	lo = min(max(lo, 0), total)
	end := max(min(hi+1, total), lo)
	page := hits[lo:end]

	results := make([]*wbxml.Node, 0, len(page))
	for _, h := range page {
		collID := strconv.FormatInt(h.fid, 10)
		sid := strconv.FormatUint(uint64(h.m.UID), 10)
		raw, _ := st.GetMessageRaw(h.fid, h.m.UID)
		appdata := emailAppData(mailRender{st: st, pref: pref, protocol: sess.protocol}, raw, h.m, collID, sid)
		results = append(results, wbxml.Elem(wbxml.SRResult,
			wbxml.Str(wbxml.ASClass, "Email"),
			wbxml.Str(wbxml.SRLongId, collID+":"+sid),
			wbxml.Str(wbxml.ASCollectionID, collID),
			&wbxml.Node{Tag: wbxml.SRProperties, Children: appdata.Children}))
	}

	rangeHi := lo
	if len(page) > 0 {
		rangeHi = lo + len(page) - 1
	}
	writeWBXML(w, mailboxSearchReply(srStoreOK, results, lo, rangeHi, total))
}

// searchHit is one Mailbox-search match: the folder it lives in and its index row.
type searchHit struct {
	fid int64
	m   objectstore.MessageInfo
}

// mailboxSearchHits scans the caller's mail folders for messages matching the
// (already lowercased) freetext and returns them sorted newest-first. A non-empty
// folderFilter (a numeric CollectionId) narrows the scan to that one folder; the
// caller slices the requested Range out of the returned set, so the sort makes the
// stateless paging deterministic. Shared by the Search and Find commands.
func mailboxSearchHits(st *objectstore.Store, freetext, folderFilter string) []searchHit {
	folders := mailboxSearchFolders()
	if fid, err := strconv.ParseInt(folderFilter, 10, 64); err == nil {
		folders = []int64{fid}
	}
	var hits []searchHit
	for _, fid := range folders {
		msgs, err := st.ListMessages(fid)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			if messageMatches(st, fid, m, freetext) {
				hits = append(hits, searchHit{fid, m})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].m.InternalDate.After(hits[j].m.InternalDate)
	})
	return hits
}

// mailboxSearchFolders is the well-known mail folder set a Store=Mailbox search
// scans when the query names no specific CollectionId.
func mailboxSearchFolders() []int64 {
	return []int64{
		mapi.PrivateFIDInbox,
		mapi.PrivateFIDSentItems,
		mapi.PrivateFIDDraft,
		mapi.PrivateFIDDeletedItems,
		mapi.PrivateFIDJunk,
	}
}

// messageMatches reports whether a message matches the (already lowercased) query.
// Subject and sender come from the folder index (no message read); only when they
// miss is the body read and scanned, the same subject/sender-then-body order the
// webmail search uses.
func messageMatches(st *objectstore.Store, fid int64, m objectstore.MessageInfo, query string) bool {
	if strings.Contains(strings.ToLower(m.Subject+" "+m.Sender), query) {
		return true
	}
	raw, err := st.GetMessageRaw(fid, m.UID)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(messageBodyText(raw)), query)
}

// messageBodyText returns the message's best text body (HTML preferred, then plain)
// for a full-text match, mirroring the webmail search's body extraction.
func messageBodyText(raw []byte) string {
	var plain, html string
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if p == nil {
			return
		}
		collectBodyPart(p, &plain, &html)
		for _, ch := range p.Children {
			walk(ch)
		}
	}
	walk(mime.ParseStructure(raw))
	if html != "" {
		return html
	}
	return plain
}

// collectBodyPart records the first plain and the first HTML text part it sees,
// skipping anything the message marks as an attachment.
func collectBodyPart(p *mime.Part, plain, html *string) {
	if p.Type != "text" || p.Disposition == "attachment" {
		return
	}
	c, err := p.DecodedContent()
	if err != nil {
		return
	}
	switch {
	case p.Subtype == "html" && *html == "":
		*html = string(c)
	case p.Subtype == "plain" && *plain == "":
		*plain = string(c)
	}
}

// searchRangeMax is the most rows one Mailbox or GAL Search may ask for
// ([MS-ASCMD] 2.2.3.143.3 Range (Search): default 0-99, maximum 100).
const searchRangeMax = 100

// srStoreRangeExceeded is the Store Status for a Range asking for more rows than
// the store's maximum ([MS-ASCMD] 2.2.3.143.3, Status 12).
const srStoreRangeExceeded = 12

// searchRange reads a store's Options>Range, "m-n" with zero-based indexes
// ([MS-ASCMD] 2.2.3.143.3). No Range is the default 0-99. A value that is not two
// numbers with m <= n is an invalid search parameter (Store Status 2), and one
// asking for more than searchRangeMax rows exceeds the maximum range (Status 12).
// A range that starts past 0 is a later page and is allowed, up to the index
// bound the same specification sets for Find (findRangeMax); Range (Search)
// names none, and an unbounded index would overflow hi+1 and ask the address
// book for any number of rows. It returns the
// bounds and srStoreOK, or the Store Status to answer.
func searchRange(store *wbxml.Node) (int, int, int) {
	opts := store.Child(wbxml.SROptions)
	if opts == nil || opts.Child(wbxml.SRRange) == nil {
		return 0, searchRangeMax - 1, srStoreOK
	}
	lo, hi, ok := strings.Cut(opts.ChildText(wbxml.SRRange), "-")
	if !ok {
		return 0, 0, srStoreReqInvalid
	}
	l, errl := strconv.Atoi(strings.TrimSpace(lo))
	h, errh := strconv.Atoi(strings.TrimSpace(hi))
	switch {
	case errl != nil || errh != nil || l < 0 || h < l:
		return 0, 0, srStoreReqInvalid
	case h-l >= searchRangeMax:
		return 0, 0, srStoreRangeExceeded
	case h > findRangeMax:
		return 0, 0, srStoreReqInvalid
	}
	return l, h, srStoreOK
}

// descendantText returns the text of the first descendant carrying tag, searching
// the subtree depth-first. The mailbox query nests FreeText (and an optional
// CollectionId folder filter) inside Query>And, so a recursive lookup avoids
// assuming a fixed nesting depth.
func descendantText(n *wbxml.Node, tag wbxml.Tag) string {
	if n == nil {
		return ""
	}
	for _, ch := range n.Children {
		if ch.Tag == tag {
			return ch.Text
		}
		if t := descendantText(ch, tag); t != "" {
			return t
		}
	}
	return ""
}

// handleGALSearch resolves a GAL Query (the text content of the Query element)
// against the directory address book and returns the rows its Range names.
func (s *Server) handleGALSearch(w http.ResponseWriter, store *wbxml.Node, sess *session) {
	lo, hi, status := searchRange(store)
	if status != srStoreOK {
		writeWBXML(w, searchGALReply(status, nil, 0, 0))
		return
	}
	query := store.ChildText(wbxml.SRQuery)
	var entries []directory.GALEntry
	if gal, ok := s.accounts.(directory.GAL); ok && query != "" {
		var err error
		if entries, err = gal.SearchGAL(sess.user, query, hi+1, directory.HideFromGAL); err != nil {
			// A directory failure is a server error, never an empty address book.
			s.logSearchFail(sess, err)
			writeWBXML(w, searchGALReply(srStoreServerError, nil, 0, 0))
			return
		}
		// Withhold the addresses the operator hid from the address book.
		entries = directory.VisibleGAL(entries)
	}
	total := len(entries)
	lo = min(lo, total)
	writeWBXML(w, searchGALReply(srStoreOK, entries[lo:min(hi+1, total)], lo, total))
}

// searchGALReply builds a Search reply for the GAL store: the overall success, then
// a Store carrying its status, a Result per GAL match in the page starting at
// index lo, and (when non-empty) the returned Range and Total.
func searchGALReply(storeStatus int, entries []directory.GALEntry, lo, total int) *wbxml.Node {
	store := []*wbxml.Node{wbxml.Str(wbxml.SRStatus, strconv.Itoa(storeStatus))}
	for _, e := range entries {
		// FirstName/LastName are emitted (empty) because some clients will not
		// render an entry without them; the GALEntry model carries no name parts.
		store = append(store, wbxml.Elem(wbxml.SRResult,
			wbxml.Elem(wbxml.SRProperties,
				wbxml.Str(wbxml.GALDisplayName, e.DisplayName),
				wbxml.Str(wbxml.GALFirstName, ""),
				wbxml.Str(wbxml.GALLastName, ""),
				wbxml.Str(wbxml.GALEmailAddress, e.Address))))
	}
	if len(entries) > 0 {
		store = append(store,
			wbxml.Str(wbxml.SRRange, strconv.Itoa(lo)+"-"+strconv.Itoa(lo+len(entries)-1)),
			wbxml.Str(wbxml.SRTotal, strconv.Itoa(total)))
	}
	return wbxml.Elem(wbxml.SRSearch,
		wbxml.Str(wbxml.SRStatus, strconv.Itoa(srStatusOK)),
		wbxml.Elem(wbxml.SRResponse, wbxml.Elem(wbxml.SRStore, store...)))
}

// mailboxSearchReply builds a Search reply for the Mailbox store: the overall
// success, then a Store carrying its status, the rendered Results, and (when any
// matched) the returned Range and Total.
func mailboxSearchReply(storeStatus int, results []*wbxml.Node, lo, hi, total int) *wbxml.Node {
	store := []*wbxml.Node{wbxml.Str(wbxml.SRStatus, strconv.Itoa(storeStatus))}
	store = append(store, results...)
	if total > 0 {
		store = append(store,
			wbxml.Str(wbxml.SRRange, strconv.Itoa(lo)+"-"+strconv.Itoa(hi)),
			wbxml.Str(wbxml.SRTotal, strconv.Itoa(total)))
	}
	return wbxml.Elem(wbxml.SRSearch,
		wbxml.Str(wbxml.SRStatus, strconv.Itoa(srStatusOK)),
		wbxml.Elem(wbxml.SRResponse, wbxml.Elem(wbxml.SRStore, store...)))
}

// searchStatus builds a bare Search status reply (e.g. a request that named no
// store to search).
func searchStatus(code int) *wbxml.Node {
	return wbxml.Elem(wbxml.SRSearch, wbxml.Str(wbxml.SRStatus, strconv.Itoa(code)))
}
