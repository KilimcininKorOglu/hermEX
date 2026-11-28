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

// TestPrintableStoreNameKeepsTheWholeLocalPart: the store's printable name is the
// owner address's local part ([MS-OXOABK] 2.2.3.7), everything before the last
// '@', since a quoted local part may hold one (RFC 5321 4.1.2).
func TestPrintableStoreNameKeepsTheWholeLocalPart(t *testing.T) {
	var props mapi.PropertyValues
	addPrintableName(&props, nil, &logonIdentity{owner: "x@other.test@hermex.test"})
	wantProp(t, props, mapi.PrAddressBookDisplayNamePrintable, "x@other.test", "printable name")
}

// storeRowAndStore logs on as owner, reads the given store columns and returns
// them with the store the logon opened.
func storeRowAndStore(t *testing.T, dir, owner string, cols []mapi.PropTag, opts ...SessionOption) (mapi.PropertyValues, *objectstore.Store) {
	t.Helper()
	accs := directory.StaticAccounts{owner: {Password: "x", MailboxPath: dir}}
	sess := NewSession(dir, accs, owner, opts...)
	t.Cleanup(sess.Close)
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	out := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), h[0])
	return decodeRow(t, ropOK(t, out, ropGetPropertiesSpecific, "GetPropertiesSpecific(store)"), cols), sess.get(h[0]).store
}

// storeRowOnly logs on as owner and reads the given store columns.
func storeRowOnly(t *testing.T, dir, owner string, cols []mapi.PropTag, opts ...SessionOption) mapi.PropertyValues {
	t.Helper()
	accs := directory.StaticAccounts{owner: {Password: "x", MailboxPath: dir}}
	sess := NewSession(dir, accs, owner, opts...)
	t.Cleanup(sess.Close)
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	out := dispatchOn(sess, buildGetProps(ropGetPropertiesSpecific, 0, cols), h[0])
	return decodeRow(t, ropOK(t, out, ropGetPropertiesSpecific, "GetPropertiesSpecific(store)"), cols)
}

// TestStoreReportsWhatItHolds proves the logon's store reports its live message
// count and size ([MS-OXCSTOR] 2.2.2.1.1.6 and 2.2.2.1.1.10) instead of the zero
// written when the mailbox was created, and names its owner and the logged-on user
// with the entry ids the address book serves.
func TestStoreReportsWhatItHolds(t *testing.T) {
	dir := provisionedDir(t)
	const owner = "owner@hermex.test"
	seedInboxMessage(t, dir, "one")
	seedInboxMessage(t, dir, "two")
	cols := []mapi.PropTag{mapi.PrContentCount, mapi.PrMessageSizeExtended, mapi.PrMailboxOwnerEntryID,
		mapi.PrUserEntryID, mapi.PrMailboxOwnerName}
	row, store := storeRowAndStore(t, dir, owner, cols)

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
		v, ok := row.Get(tag)
		if !ok {
			t.Errorf("%v missing from store row", tag)
			continue
		}
		b, ok := v.([]byte)
		if !ok || !bytes.Equal(b, nspi.MailUserEntryID(owner)) {
			t.Errorf("%v = %x, want the owner's address-book entry id", tag, v)
		}
	}
	wantProp(t, row, mapi.PrMailboxOwnerName, owner, "owner name")
}

// TestStoreReportsAPrintableOwnerName proves the logon's store serves
// PidTagAddressBookDisplayNamePrintable as the local part of the owner's address,
// the printable name a client shows where the display name may not render.
func TestStoreReportsAPrintableOwnerName(t *testing.T) {
	cols := []mapi.PropTag{mapi.PrAddressBookDisplayNamePrintable}
	row := storeRowOnly(t, provisionedDir(t), "owner@hermex.test", cols)
	wantProp(t, row, mapi.PrAddressBookDisplayNamePrintable, "owner", "printable name")
}

// TestStoreReportsTheConnectLocale proves the logon reports the locale the client
// connected with ([MS-OXCSTOR] 2.2.2.1.1.12, .14 and .15), and leaves a locale it
// was not given unset, which a read answers with NotFound ([MS-OXCSTOR] 3.2.5.1.1).
func TestStoreReportsTheConnectLocale(t *testing.T) {
	const owner = "owner@hermex.test"
	cols := []mapi.PropTag{mapi.PrLocaleID, mapi.PrSortLocaleID, mapi.PrCodePageID}
	row := storeRowOnly(t, provisionedDir(t), owner, cols,
		WithLocale(Locale{CodePage: 1254, LCIDString: 0x041F, LCIDSort: 0x0409}))
	wantProp(t, row, mapi.PrLocaleID, int32(0x041F), "locale")
	wantProp(t, row, mapi.PrSortLocaleID, int32(0x0409), "sort locale")
	wantProp(t, row, mapi.PrCodePageID, int32(1254), "code page")

	row = storeRowOnly(t, provisionedDir(t), owner, cols)
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
	row, store := storeRowAndStore(t, provisionedDir(t), "owner@hermex.test", []mapi.PropTag{mapi.PrSerializedReplidGuidMap})
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
	v, ok := row.Get(mapi.PrSerializedReplidGuidMap)
	if !ok {
		t.Fatal("PrSerializedReplidGuidMap missing from store row")
	}
	got, ok := v.([]byte)
	if !ok || !bytes.Equal(got, want.Bytes()) {
		t.Errorf("replid map = % x, want % x", got, want.Bytes())
	}
}

// TestStoreHidesAnUnlimitedQuota proves a quota stored as 0 (unlimited) is not
// served: [MS-OXCSTOR] 2.2.2.1.1.3 expresses no limit as an unset property, and a
// client reading 0 would call the mailbox full. A set quota is served in KB.
func TestStoreHidesAnUnlimitedQuota(t *testing.T) {
	dir := provisionedDir(t)
	const owner = "owner@hermex.test"
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuota(objectstore.QuotaLimits{SendKB: 0, ReceiveKB: 2048}); err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)

	row := storeRowOnly(t, dir, owner, []mapi.PropTag{mapi.PrProhibitSendQuota, mapi.PrProhibitReceiveQuota})
	if v, ok := row.Get(mapi.PrProhibitSendQuota); ok {
		t.Errorf("an unlimited send quota was served as %v", v)
	}
	wantProp(t, row, mapi.PrProhibitReceiveQuota, int32(2048), "receive quota")
}
