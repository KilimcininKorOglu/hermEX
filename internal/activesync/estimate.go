package activesync

import (
	"net/http"
	"strconv"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// GetItemEstimate status codes (MS-ASCMD): 1 success, 2 the collection's sync
// key is not primed or does not match (the client must Sync first).
const (
	estimateStatusOK        = 1
	estimateStatusNotPrimed = 2
)

// handleGetItemEstimate answers GetItemEstimate: per collection it reports how
// many changes the next Sync would carry, computed by the same snapshot diff —
// purely a progress-bar hint, so it never advances any state.
func (s *Server) handleGetItemEstimate(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	collections := root.Child(wbxml.GIECollections)
	if collections == nil {
		http.Error(w, "GetItemEstimate without collections", http.StatusBadRequest)
		return
	}

	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()
	state, err := loadState(st)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dev := state.device(sess.req.deviceID)

	var responses []*wbxml.Node
	for _, c := range collections.Children {
		if c.Tag != wbxml.GIECollection {
			continue
		}
		// The collection id and sync key may arrive on either the GetItemEstimate
		// or the AirSync code page depending on the client; accept both.
		collID := c.ChildText(wbxml.ASCollectionID)
		if collID == "" {
			collID = c.ChildText(wbxml.GIECollectionID)
		}
		clientKey := c.ChildText(wbxml.ASSyncKey)
		cstate := dev.collection(collID)

		folderID, perr := strconv.ParseInt(collID, 10, 64)
		if collID == "" || cstate.SyncKey == "" || clientKey != cstate.SyncKey || perr != nil {
			responses = append(responses, estimateResponse(collID, estimateStatusNotPrimed, 0))
			continue
		}
		// The calendar collection is versioned by object change number, not the IMAP
		// index, so its estimate must read the same object list Sync does.
		if folderID == int64(mapi.PrivateFIDCalendar) {
			objs, err := st.ListFolderObjects(folderID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			responses = append(responses, estimateResponse(collID, estimateStatusOK, calendarChangeCount(cstate.Items, objs)))
			continue
		}
		live, err := st.ListMessages(folderID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		responses = append(responses, estimateResponse(collID, estimateStatusOK, len(diffSnapshot(cstate.Items, live))))
	}
	writeWBXML(w, wbxml.Elem(wbxml.GIEGetItemEstimate, responses...))
}

// estimateResponse builds one GetItemEstimate Response element.
func estimateResponse(collID string, status, estimate int) *wbxml.Node {
	return wbxml.Elem(wbxml.GIEResponse,
		wbxml.Str(wbxml.GIEStatus, strconv.Itoa(status)),
		wbxml.Elem(wbxml.GIECollection,
			wbxml.Str(wbxml.GIECollectionID, collID),
			wbxml.Str(wbxml.GIEEstimate, strconv.Itoa(estimate)),
		),
	)
}
