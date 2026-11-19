package webmail2api

import (
	"net/http"

	"hermex/internal/objectstore"
)

// refuseOverQuota answers 507 Insufficient Storage and reports true when st is
// past its receive quota, the limit past which nothing new may enter a mailbox,
// whether by delivery or by the user creating an item. A store that cannot report
// its usage answers 500. Editing or deleting what the mailbox holds stays open,
// so only the handlers that add an item call it.
func refuseOverQuota(w http.ResponseWriter, st *objectstore.Store) bool {
	over, err := st.OverReceiveQuota()
	switch {
	case err != nil:
		logError("quota-read", err, nil)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read the mailbox quota"})
		return true
	case over:
		writeJSON(w, http.StatusInsufficientStorage, map[string]string{"error": "mailbox is over its quota"})
		return true
	}
	return false
}
