package rop

import (
	"bytes"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/nspi"
	"hermex/internal/objectstore"
)

// storeRow logs on as owner and reads the given store columns.
func storeRow(t *testing.T, dir, owner string, cols []mapi.PropTag, opts ...SessionOption) (mapi.PropertyValues, *objectstore.Store) {
	t.Helper()
	accs := directory.StaticAccounts{owner: {Password: "x", MailboxPath: dir}}
	sess := NewSession(dir, accs, owner, opts...)
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

// TestStoreReportsTheConnectLocale proves the logon reports the locale the client
// connected with ([MS-OXCSTOR] 2.2.2.1.1.12, .14 and .15), and leaves a locale it
// was not given unset, which a read answers with NotFound ([MS-OXCSTOR] 3.2.5.1.1).
func TestStoreReportsTheConnectLocale(t *testing.T) {
	const owner = "owner@hermex.test"
	cols := []mapi.PropTag{mapi.PrLocaleID, mapi.PrSortLocaleID, mapi.PrCodePageID}
	row, _ := storeRow(t, t.TempDir(), owner, cols,
		WithLocale(Locale{CodePage: 1254, LCIDString: 0x041F, LCIDSort: 0x0409}))
	wantProp(t, row, mapi.PrLocaleID, int32(0x041F), "locale")
	wantProp(t, row, mapi.PrSortLocaleID, int32(0x0409), "sort locale")
	wantProp(t, row, mapi.PrCodePageID, int32(1254), "code page")

	row, _ = storeRow(t, t.TempDir(), owner, cols)
	for _, tag := range cols {
		if v, ok := row.Get(tag); ok {
			t.Errorf("%#x = %v without a connect locale, want it unset", uint32(tag), v)
		}
	}
}

// TestStoreReportsItsReplidMap proves the logon serves its whole REPLID/REPLGUID
// mapping in PidTagSerializedReplidGuidMap ([MS-OXCSTOR] 2.2.2.1.1.13): the object
// replid with the store GUID, and the replid the logon reports with the mapping
// signature it pairs that replid with.
func TestStoreReportsItsReplidMap(t *testing.T) {
	row, store := storeRow(t, t.TempDir(), "owner@hermex.test", []mapi.PropTag{mapi.PrSerializedReplidGuidMap})
	storeGUID, err := store.StoreGUID()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := store.MappingSignature()
	if err != nil {
		t.Fatal(err)
	}
	want := ext.NewPush(0)
	want.Uint16(1)
	want.GUID(storeGUID)
	want.Uint16(privateReplID)
	want.GUID(sig)
	v, _ := row.Get(mapi.PrSerializedReplidGuidMap)
	if got, _ := v.([]byte); !bytes.Equal(got, want.Bytes()) {
		t.Errorf("replid map = % x, want % x", got, want.Bytes())
	}
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
