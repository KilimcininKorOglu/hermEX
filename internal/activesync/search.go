package activesync

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/wbxml"
)

// galSearchLimit caps the GAL matches a single Search returns.
const galSearchLimit = 100

// Search Status values (MS-ASCMD 2.2.3.166.3): the overall command status and the
// per-store status carried in the response.
const (
	srStatusOK          = 1 // the command succeeded
	srStatusServerError = 3 // the request carried no store to search
	srStoreOK           = 1 // the store search succeeded
	srStoreReqInvalid   = 2 // the store/query is not supported here
)

// handleSearch answers Search ([MS-ASCMD] 2.2.2.16) for the GAL store: the Query
// is resolved against the directory GAL and the reply carries a Result per match
// with its display name and address, plus the result Range and Total a device
// needs to display them. v1 serves only the GAL store — a Mailbox or
// DocumentLibrary search reports a request-invalid store status rather than
// silently claiming an empty result.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request, _ *session) {
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
	query := store.ChildText(wbxml.SRQuery)

	if !strings.EqualFold(name, "GAL") {
		writeWBXML(w, searchStoreReply(srStoreReqInvalid, nil))
		return
	}

	var entries []directory.GALEntry
	if gal, ok := s.accounts.(directory.GAL); ok && query != "" {
		entries, _ = gal.SearchGAL(query, galSearchLimit)
	}
	writeWBXML(w, searchStoreReply(srStoreOK, entries))
}

// searchStoreReply builds a Search reply for one store: the overall success, then
// a Store carrying its status, a Result per GAL match, and (when non-empty) the
// returned Range and Total.
func searchStoreReply(storeStatus int, entries []directory.GALEntry) *wbxml.Node {
	store := []*wbxml.Node{wbxml.Str(wbxml.SRStatus, strconv.Itoa(storeStatus))}
	for _, e := range entries {
		store = append(store, wbxml.Elem(wbxml.SRResult,
			wbxml.Elem(wbxml.SRProperties,
				wbxml.Str(wbxml.GALDisplayName, e.DisplayName),
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

// searchStatus builds a bare Search status reply (e.g. a request that named no
// store to search).
func searchStatus(code int) *wbxml.Node {
	return wbxml.Elem(wbxml.SRSearch, wbxml.Str(wbxml.SRStatus, strconv.Itoa(code)))
}
