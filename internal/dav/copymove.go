package dav

import (
	"net/http"
	"net/url"
	"strings"

	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
	"hermex/internal/oxcmail"
	"hermex/internal/oxvcard"
)

// handleCopyMove implements COPY and MOVE for a calendar or address-book object
// (RFC 4918 §9.8/§9.9). The object is re-created in the destination collection
// through the same iCal/vCard path a PUT uses — so the copy gets a fresh identity
// rather than duplicating the source's — and for MOVE the source is then removed.
// Collection-level COPY/MOVE is not supported.
func (s *Server) handleCopyMove(w http.ResponseWriter, r *http.Request, mailbox string, move bool) {
	srcKind, _, srcColl, srcName := classify(r.URL.Path)
	isCal := strings.HasPrefix(r.URL.Path, "/dav/calendars/")
	isCard := strings.HasPrefix(r.URL.Path, "/dav/addressbooks/")
	wantKind, ext := kindObject, ".vcf"
	if isCal {
		wantKind, ext = kindCalObject, ".ics"
	}
	if (!isCal && !isCard) || srcKind != wantKind {
		http.Error(w, "COPY/MOVE is supported only on calendar/contact objects", http.StatusForbidden)
		return
	}

	dest := r.Header.Get("Destination")
	if dest == "" {
		http.Error(w, "missing Destination header", http.StatusBadRequest)
		return
	}
	destPath := dest
	if u, err := url.Parse(dest); err == nil && u.Path != "" {
		destPath = u.Path
	}
	dstKind, _, dstColl, dstName := classify(destPath)
	// The destination must be the same kind of object under the same protocol root.
	if dstKind != wantKind || strings.HasPrefix(destPath, "/dav/calendars/") != isCal {
		http.Error(w, "destination must be the same kind of collection", http.StatusForbidden)
		return
	}

	st, err := objectstore.Open(mailbox)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()

	srcFID, ok, err := collectionByKind(st, isCal, srcColl)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such source collection", http.StatusNotFound)
		return
	}
	dstFID, ok, err := collectionByKind(st, isCal, dstColl)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		// The destination collection must already exist (RFC 4918 §9.8.5: 409).
		http.Error(w, "destination collection does not exist", http.StatusConflict)
		return
	}
	if srcFID == dstFID && srcName == dstName {
		http.Error(w, "source and destination are the same resource", http.StatusForbidden)
		return
	}

	srcObj, found, err := findObjectByName(st, srcFID, ext, srcName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "source not found", http.StatusNotFound)
		return
	}

	dstObj, dstExists, err := findObjectByName(st, dstFID, ext, dstName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if dstExists && r.Header.Get("Overwrite") == "F" {
		http.Error(w, "destination exists", http.StatusPreconditionFailed)
		return
	}

	// Re-serialize through the object's own format so the copy is a fresh message.
	tag, _, err := resourceNameTag(st, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var msg *oxcmail.Message
	if isCal {
		data, err := calendarData(st, srcObj.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		msg, err = oxcical.Import([]byte(data), icalOptions(st))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		data, err := addressData(st, srcObj.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		msg, err = oxvcard.Import([]byte(data), vcardOptions(st))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	msg.Props.Set(tag, dstName)

	// Replace an existing destination (the new copy takes its place).
	if dstExists {
		if err := st.DeleteObject(dstObj.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if _, err := st.CreateMessage(dstFID, msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if move {
		// Route the source through the dumpster so sync-collection reports a tombstone.
		if err := st.SoftDeleteObject(srcObj.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if dstExists {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

// collectionByKind resolves a collection name in the calendar or contacts root.
func collectionByKind(st *objectstore.Store, isCal bool, coll string) (int64, bool, error) {
	if isCal {
		return calCollectionFID(st, coll)
	}
	return cardCollectionFID(st, coll)
}
