package rop

import (
	"bytes"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/nspi"
	"hermex/internal/objectstore"
)

// storeRow logs on as owner and reads the given store columns.
func storeRow(t *testing.T, dir, owner string, cols []mapi.PropTag) (mapi.PropertyValues, *objectstore.Store) {
	t.Helper()
	accs := directory.StaticAccounts{owner: {Password: "x", MailboxPath: dir}}
	sess := NewSession(dir, accs, owner)
	t.Cleanup(sess.Close)
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	out, _ := sess.Dispatch(buildGetProps(ropGetPropertiesSpecific, 0, cols), []uint32{h[0]})
	return decodeRow(t, ropOK(t, out, ropGetPropertiesSpecific, "GetPropertiesSpecific(store)"), cols), sess.get(h[0]).store
}

// TestStoreReportsWhatItHolds proves the logon's store reports its live message
// count and size ([MS-OXCSTOR] 2.2.2.1.1.6 and 2.2.2.1.1.10) instead of the zero
// written when the mailbox was created, and names its owner and the logged-on user
// with the entry ids the address book serves.
func TestStoreReportsWhatItHolds(t *testing.T) {
	dir := t.TempDir()
	const owner = "owner@hermex.test"
	seedInboxMessage(t, dir, "one")
	seedInboxMessage(t, dir, "two")
	cols := []mapi.PropTag{mapi.PrContentCount, mapi.PrMessageSizeExtended, mapi.PrMailboxOwnerEntryID,
		mapi.PrUserEntryID, mapi.PrMailboxOwnerName}
	row, store := storeRow(t, dir, owner, cols)

	wantProp(t, row, mapi.PrContentCount, int32(2), "store content count")
	size, err := store.MailboxSize()
	if err != nil {
		t.Fatal(err)
	}
	if size == 0 {
		t.Fatal("seeded mailbox has no size")
	}
	wantProp(t, row, mapi.PrMessageSizeExtended, size, "store size")
	for _, tag := range []mapi.PropTag{mapi.PrMailboxOwnerEntryID, mapi.PrUserEntryID} {
		if v, _ := row.Get(tag); !bytes.Equal(v.([]byte), nspi.MailUserEntryID(owner)) {
			t.Errorf("%v = %x, want the owner's address-book entry id", tag, v)
		}
	}
	wantProp(t, row, mapi.PrMailboxOwnerName, owner, "owner name")
}

// TestStoreHidesAnUnlimitedQuota proves a quota stored as 0 (unlimited) is not
// served: [MS-OXCSTOR] 2.2.2.1.1.3 expresses no limit as an unset property, and a
// client reading 0 would call the mailbox full. A set quota is served in KB.
func TestStoreHidesAnUnlimitedQuota(t *testing.T) {
	dir := t.TempDir()
	const owner = "owner@hermex.test"
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuota(objectstore.QuotaLimits{SendKB: 0, ReceiveKB: 2048}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	row, _ := storeRow(t, dir, owner, []mapi.PropTag{mapi.PrProhibitSendQuota, mapi.PrProhibitReceiveQuota})
	if v, ok := row.Get(mapi.PrProhibitSendQuota); ok {
		t.Errorf("an unlimited send quota was served as %v", v)
	}
	wantProp(t, row, mapi.PrProhibitReceiveQuota, int32(2048), "receive quota")
}
