package rop

import (
	"errors"

	"hermex/internal/ext"
	"hermex/internal/objectstore"
)

// errQuotaExceeded refuses a write that would add to a mailbox past its receive
// quota; folderCopyError maps it to ecQuotaExceeded.
var errQuotaExceeded = errors.New("rop: mailbox is over its quota")

// refuseOverQuota writes the failure response for rop and reports true when st
// is past its receive quota, the limit past which nothing new may enter the
// mailbox, whether by delivery or by a client adding an item. The answer is
// ecQuotaExceeded ([MS-OXCDATA] 2.4.1), or ecError when the store cannot report
// its usage. Only the paths that add an item call it, so a full mailbox can still
// edit, move and delete what it holds.
func refuseOverQuota(out *ext.Push, rop, hindex uint8, st *objectstore.Store) bool {
	over, err := st.OverReceiveQuota()
	switch {
	case err != nil:
		writeErr(out, rop, hindex, ecError)
		return true
	case over:
		writeErr(out, rop, hindex, ecQuotaExceeded)
		return true
	}
	return false
}
