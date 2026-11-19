package dav

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"

	"hermex/internal/objectstore"
)

// errQuotaExceeded refuses a write that would add an object to a mailbox past
// its receive quota, the limit past which nothing new may enter it.
var errQuotaExceeded = errors.New("mailbox is over its quota")

// checkQuota returns errQuotaExceeded when st is past its receive quota, or the
// store's error when it cannot report its usage.
func checkQuota(st *objectstore.Store) error {
	over, err := st.OverReceiveQuota()
	if err != nil {
		return err
	}
	if over {
		return errQuotaExceeded
	}
	return nil
}

// writeQuotaExceeded answers 507 Insufficient Storage with the
// DAV:quota-not-exceeded precondition RFC 4331 section 6 names for a request
// that would take a collection past its quota.
func writeQuotaExceeded(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusInsufficientStorage)
	// Final response body; a write failure means the client is gone, with no recourse.
	_, _ = fmt.Fprintf(w, "%s<D:error xmlns:D=\"DAV:\"><D:quota-not-exceeded/></D:error>\n", xml.Header)
}
