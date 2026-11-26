package objectstore

import (
	"path/filepath"
	"reflect"
	"testing"

	"hermex/internal/mapi"
)

// tag builds a property tag from a property id and type.
func tag(propid uint16, t mapi.PropType) mapi.PropTag {
	return mapi.PropTag(uint32(propid)<<16 | uint32(t))
}

// allTypeProps is one value of every storage class: native scalar columns plus
// the ext-blob path (PtCLSID).
func allTypeProps() mapi.PropertyValues {
	return mapi.PropertyValues{
		{Tag: tag(0x10, mapi.PtBoolean), Value: true},
		{Tag: tag(0x11, mapi.PtShort), Value: int16(-3)},
		{Tag: tag(0x12, mapi.PtLong), Value: int32(70000)},
		{Tag: tag(0x13, mapi.PtError), Value: uint32(0x80004005)},
		{Tag: tag(0x14, mapi.PtI8), Value: int64(1) << 40},
		{Tag: tag(0x15, mapi.PtCurrency), Value: int64(12345)},
		{Tag: tag(0x16, mapi.PtSysTime), Value: uint64(0x01D7AC1F2E3D4C5B)},
		{Tag: tag(0x17, mapi.PtFloat), Value: float32(3.5)},
		{Tag: tag(0x18, mapi.PtDouble), Value: float64(2.5)},
		{Tag: tag(0x19, mapi.PtAppTime), Value: float64(45000.25)},
		{Tag: tag(0x1A, mapi.PtString8), Value: "ascii-string"},
		{Tag: tag(0x1B, mapi.PtUnicode), Value: "ünïçödé"},
		{Tag: tag(0x1C, mapi.PtBinary), Value: []byte{0x00, 0x01, 0x02, 0xFF}},
		{Tag: tag(0x1D, mapi.PtCLSID), Value: mapi.GUID{Data1: 0x01020304, Data2: 0x0506, Data3: 0x0708, Data4: [8]byte{9, 10, 11, 12, 13, 14, 15, 16}}},
	}
}

func asMap(pv mapi.PropertyValues) map[mapi.PropTag]any {
	m := make(map[mapi.PropTag]any, len(pv))
	for _, p := range pv {
		m[p.Tag] = p.Value
	}
	return m
}

// openTestStore opens a bare object store (no built-in folders) so allocator
// and property-table tests run against a controlled baseline.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := open(filepath.Join(t.TempDir(), "mbox"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// openSeededStore opens a fully provisioned mailbox (the default folder
// hierarchy seeded), as real callers get from Open.
func openSeededStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "mbox"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStorePropertiesRoundTrip(t *testing.T) {
	s := openTestStore(t)
	want := allTypeProps()
	mustNoErr(t, "set store properties", s.SetStoreProperties(want))
	got, err := s.GetStoreProperties()
	mustNoErr(t, "get store properties", err)
	wantSameValues(t, want, asMap(got))

	// Filtered read returns only the requested tags.
	sub, err := s.GetStoreProperties(tag(0x1A, mapi.PtString8), tag(0x1C, mapi.PtBinary))
	mustNoErr(t, "filtered get", err)
	wantEq(t, "props returned by the filtered get", len(sub), 2)
	subMap := asMap(sub)
	for _, tg := range []mapi.PropTag{tag(0x1A, mapi.PtString8), tag(0x1C, mapi.PtBinary)} {
		if _, ok := subMap[tg]; !ok {
			t.Errorf("filtered get dropped the requested %s and returned %v", tg, sub)
		}
	}

	// Overwrite replaces the value (upsert).
	mustNoErr(t, "upsert store property",
		s.SetStoreProperties(mapi.PropertyValues{{Tag: tag(0x12, mapi.PtLong), Value: int32(-1)}}))
	again, err := s.GetStoreProperties(tag(0x12, mapi.PtLong))
	mustNoErr(t, "read the upserted store property", err)
	if len(again) != 1 || again[0].Value != int32(-1) {
		t.Errorf("upsert: got %#v, want int32(-1)", again)
	}
}

// wantSameValues checks every written property came back with its value intact.
func wantSameValues(t *testing.T, want mapi.PropertyValues, got map[mapi.PropTag]any) {
	t.Helper()
	for _, w := range want {
		g, ok := got[w.Tag]
		if !ok {
			t.Errorf("%s missing after round-trip", w.Tag)
			continue
		}
		if !reflect.DeepEqual(g, w.Value) {
			t.Errorf("%s = %#v (%T), want %#v (%T)", w.Tag, g, g, w.Value, w.Value)
		}
	}
}

// TestFolderPropertiesRefuseAMissingFolder: a property write aimed at a folder
// the store does not hold is refused and leaves no orphan property row.
func TestFolderPropertiesRefuseAMissingFolder(t *testing.T) {
	s := openTestStore(t)
	const missing = int64(0x7777)
	if err := s.SetFolderProperties(missing, allTypeProps()); err == nil {
		t.Error("a property write to a missing folder succeeded")
	}
	wantRows(t, s, "orphan folder property rows", 0, `SELECT COUNT(*) FROM folder_properties WHERE folder_id=?`, missing)
}

func TestFolderPropertiesRoundTrip(t *testing.T) {
	s := openTestStore(t)
	// A folder_properties row needs an existing folder (foreign key). Insert a
	// minimal folder directly; folder creation proper arrives with identity.
	if _, err := s.objdb.Exec(
		`INSERT INTO folders (folder_id, change_number, cur_eid, max_eid) VALUES (?, ?, ?, ?)`,
		mapi.PrivateFIDInbox, 1, 0x10000, 0x20000); err != nil {
		t.Fatal(err)
	}
	want := allTypeProps()
	if err := s.SetFolderProperties(mapi.PrivateFIDInbox, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFolderProperties(mapi.PrivateFIDInbox)
	if err != nil {
		t.Fatal(err)
	}
	gm := asMap(got)
	for _, w := range want {
		if g, ok := gm[w.Tag]; !ok || !reflect.DeepEqual(g, w.Value) {
			t.Errorf("%s = %#v (ok=%v), want %#v", w.Tag, g, ok, w.Value)
		}
	}
}
