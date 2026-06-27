package activesync

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// Find Status values (MS-ASCMD, Find Since 16.1): the overall command status and
// the per-Response status. The Find status set has no server-error code, so a
// store-open failure on the caller's own mailbox is reported as invalid-request.
const (
	findStatusSuccess        = 1
	findStatusInvalidRequest = 2
)

// findPreviewPref renders a bounded plain-text body for each Find hit. Find carries
// no BodyPreference (results are previews the client expands on open), so a full
// MIME per hit would bloat a paged result; a truncated plain body keeps it light.
var findPreviewPref = bodyPref{typ: bodyTypePlain, truncation: 512}

// handleFind answers Find ([MS-ASCMD], Since 16.1), the unified search a 16.x
// client issues. It serves a MailboxSearchCriterion over the caller's OWN mailbox
// (the authenticated session, never a client-supplied identity), reusing the same
// scan the Search command runs, and returns each hit's class, server id, folder id,
// and a preview of the listing Properties. A GalSearchCriterion is reported
// invalid-request (the reference does not implement GAL-in-Find either).
func (s *Server) handleFind(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	exec := root.Child(wbxml.FNDExecuteSearch)
	if exec == nil {
		writeWBXML(w, findStatus(findStatusInvalidRequest))
		return
	}
	mbx := exec.Child(wbxml.FNDMailboxSearchCriterion)
	if mbx == nil {
		writeWBXML(w, findStatus(findStatusInvalidRequest))
		return
	}
	query := mbx.Child(wbxml.FNDQuery)
	freetext := strings.ToLower(strings.TrimSpace(descendantText(query, wbxml.FNDFreeText)))
	folderFilter := descendantText(query, wbxml.ASCollectionID)

	lo, hi := 0, mailboxSearchHiDefault
	if opts := mbx.Child(wbxml.FNDOptions); opts != nil {
		lo, hi = parseSearchRange(opts.ChildText(wbxml.FNDRange), lo, hi)
	}

	if freetext == "" {
		writeWBXML(w, findReply(nil, 0, 0, 0))
		return
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		writeWBXML(w, findStatus(findStatusInvalidRequest))
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
		appdata := emailAppData(raw, h.m, collID, sid, findPreviewPref)
		results = append(results, wbxml.Elem(wbxml.FNDResult,
			wbxml.Str(wbxml.ASClass, "Email"),
			wbxml.Str(wbxml.ASServerID, sid),
			wbxml.Str(wbxml.ASCollectionID, collID),
			&wbxml.Node{Tag: wbxml.FNDProperties, Children: appdata.Children}))
	}

	rangeHi := lo
	if len(page) > 0 {
		rangeHi = lo + len(page) - 1
	}
	writeWBXML(w, findReply(results, lo, rangeHi, total))
}

// findStatus builds a bare Find reply carrying only the overall status, for a
// malformed or unsupported request.
func findStatus(code int) *wbxml.Node {
	return wbxml.Elem(wbxml.FNDFind, wbxml.Str(wbxml.FNDStatus, strconv.Itoa(code)))
}

// findReply builds a successful Find reply: the overall success, then a Response
// naming the Mailbox store, its status, the rendered Results, and (when any
// matched) the returned Range and Total.
func findReply(results []*wbxml.Node, lo, hi, total int) *wbxml.Node {
	resp := []*wbxml.Node{
		wbxml.Str(wbxml.IOStore, "Mailbox"),
		wbxml.Str(wbxml.FNDStatus, strconv.Itoa(findStatusSuccess)),
	}
	resp = append(resp, results...)
	if total > 0 {
		resp = append(resp,
			wbxml.Str(wbxml.FNDRange, strconv.Itoa(lo)+"-"+strconv.Itoa(hi)),
			wbxml.Str(wbxml.FNDTotal, strconv.Itoa(total)))
	}
	return wbxml.Elem(wbxml.FNDFind,
		wbxml.Str(wbxml.FNDStatus, strconv.Itoa(findStatusSuccess)),
		wbxml.Elem(wbxml.FNDResponse, resp...))
}
