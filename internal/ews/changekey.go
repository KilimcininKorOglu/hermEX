package ews

import (
	"hermex/internal/objectstore"
	"hermex/internal/oxews"
)

// changeKey is the EWS change key of a stored item: its change number, which
// every edit advances while the item id stays, so a client holding an older key
// can tell the item changed. A failed read is recorded and yields an empty key
// rather than a key that claims a version it does not name.
func changeKey(st *objectstore.Store, messageID int64) string {
	cn, err := st.MessageChangeNumber(messageID)
	if err != nil {
		st.LogSwallowedError("ews.change_key", err)
		return ""
	}
	return oxews.ChangeKey(cn)
}
