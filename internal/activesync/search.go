package activesync

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// galSearchLimit caps the GAL matches a single Search returns.
const galSearchLimit = 100

// mailboxSearchHiDefault is the upper Range index a Store=Mailbox search returns
// when the client requests no Range (0-99 = 100 results, the MS-ASCMD default). It
// also bounds the per-request scan: matches beyond it are counted in Total but not
// rendered.
const mailboxSearchHiDefault = 99

// Search Status values (MS-ASCMD 2.2.3.166.3): the overall command status and the
// per-store status carried in the response.
const (
	srStatusOK          = 1 // the command succeeded
	srStatusServerError = 3 // the request carried no store to search
	srStoreOK           = 1 // the store search succeeded
	srStoreReqInvalid   = 2 // the store/query is not supported here
)

// handleSearch answers Search ([MS-ASCMD] 2.2.2.16). The GAL store resolves the
// Query against the directory address book; the Mailbox store scans the caller's
// own mail folders for messages matching the query's FreeText. A DocumentLibrary
// search reports a request-invalid store status (hermEX has no document store)
// rather than silently claiming an empty result.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
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
		// The GAL query is the text content of the Query element.
		query := store.ChildText(wbxml.SRQuery)
		var entries []directory.GALEntry
		if gal, ok := s.accounts.(directory.GAL); ok && query != "" {
			entries, _ = gal.SearchGAL(query, galSearchLimit)
		}
		writeWBXML(w, searchGALReply(srStoreOK, entries))
	case strings.EqualFold(name, "Mailbox"):
		s.handleMailboxSearch(w, store, sess)
	default:
		writeWBXML(w, searchGALReply(srStoreReqInvalid, nil))
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
	lo, hi := 0, mailboxSearchHiDefault
	if opts := store.Child(wbxml.SROptions); opts != nil {
		pref = parseBodyPref(opts)
		lo, hi = parseSearchRange(opts.ChildText(wbxml.SRRange), lo, hi)
	}

	if freetext == "" {
		writeWBXML(w, mailboxSearchReply(srStoreOK, nil, 0, 0, 0))
		return
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeWBXML(w, mailboxSearchReply(srStatusServerError, nil, 0, 0, 0))
		return
	}
	defer st.Close()

	folders := mailboxSearchFolders()
	if fid, perr := strconv.ParseInt(folderFilter, 10, 64); perr == nil {
		folders = []int64{fid}
	}

	type hit struct {
		fid int64
		m   objectstore.MessageInfo
	}
	var hits []hit
	for _, fid := range folders {
		msgs, err := st.ListMessages(fid)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			if messageMatches(st, fid, m, freetext) {
				hits = append(hits, hit{fid, m})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].m.InternalDate.After(hits[j].m.InternalDate)
	})

	total := len(hits)
	lo = min(max(lo, 0), total)
	end := max(min(hi+1, total), lo)
	page := hits[lo:end]

	results := make([]*wbxml.Node, 0, len(page))
	for _, h := range page {
		collID := strconv.FormatInt(h.fid, 10)
		sid := strconv.FormatUint(uint64(h.m.UID), 10)
		raw, _ := st.GetMessageRaw(h.fid, h.m.UID)
		appdata := emailAppData(raw, h.m, collID, sid, pref)
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
	root := mime.ParseStructure(raw)
	var plain, html string
	var walk func(p *mime.Part)
	walk = func(p *mime.Part) {
		if p == nil {
			return
		}
		if p.Type == "text" && p.Disposition != "attachment" {
			if c, err := p.DecodedContent(); err == nil {
				switch {
				case p.Subtype == "html" && html == "":
					html = string(c)
				case p.Subtype == "plain" && plain == "":
					plain = string(c)
				}
			}
		}
		for _, ch := range p.Children {
			walk(ch)
		}
	}
	walk(root)
	if html != "" {
		return html
	}
	return plain
}

// parseSearchRange reads an Options>Range "lo-hi" (e.g. "0-49"). A missing or
// malformed value leaves the defaults so a client that sends no Range still gets a
// bounded page.
func parseSearchRange(s string, defLo, defHi int) (int, int) {
	lo, hi, ok := strings.Cut(s, "-")
	if !ok {
		return defLo, defHi
	}
	l, errl := strconv.Atoi(strings.TrimSpace(lo))
	h, errh := strconv.Atoi(strings.TrimSpace(hi))
	if errl != nil || errh != nil || l < 0 || h < l {
		return defLo, defHi
	}
	return l, h
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

// searchGALReply builds a Search reply for the GAL store: the overall success, then
// a Store carrying its status, a Result per GAL match, and (when non-empty) the
// returned Range and Total.
func searchGALReply(storeStatus int, entries []directory.GALEntry) *wbxml.Node {
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
			wbxml.Str(wbxml.SRRange, "0-"+strconv.Itoa(len(entries)-1)),
			wbxml.Str(wbxml.SRTotal, strconv.Itoa(len(entries))))
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
