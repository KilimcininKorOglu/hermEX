package store

import (
	"path/filepath"
	"reflect"
	"testing"

	"hermex/internal/mapi"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "store.sqlite3"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Exercises the value codec across native-scalar and ext-blob storage paths,
// proving each property type survives a store/load round trip.
func TestFolderPropertyRoundTrip(t *testing.T) {
	s := openTemp(t)
	fid, err := s.CreateFolder(nil, "Inbox")
	if err != nil {
		t.Fatal(err)
	}
	guid := mapi.GUID{Data1: 0x00062002, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	props := mapi.PropertyValues{
		{Tag: mapi.MakeTag(0x3001, mapi.PtUnicode), Value: "Inbox"},
		{Tag: mapi.MakeTag(0x3603, mapi.PtLong), Value: int32(42)},
		{Tag: mapi.MakeTag(0x6633, mapi.PtBoolean), Value: true},
		{Tag: mapi.MakeTag(0x6714, mapi.PtI8), Value: int64(1 << 40)},
		{Tag: mapi.MakeTag(0x3007, mapi.PtSysTime), Value: uint64(0x01D9A1B2C3D4E5F6)},
		{Tag: mapi.MakeTag(0x0FF9, mapi.PtBinary), Value: []byte{1, 2, 3}},
		{Tag: mapi.MakeTag(0x8001, mapi.PtMvUnicode), Value: []string{"a", "bb"}},
		{Tag: mapi.MakeTag(0x8002, mapi.PtCLSID), Value: guid},
	}
	if err := s.SetFolderProperties(fid, props); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFolderProperties(fid)
	if err != nil {
		t.Fatal(err)
	}
	// Row order is unspecified, so compare keyed by tag.
	want := map[mapi.PropTag]any{}
	for _, p := range props {
		want[p.Tag] = p.Value
	}
	gotMap := map[mapi.PropTag]any{}
	for _, p := range got {
		gotMap[p.Tag] = p.Value
	}
	if !reflect.DeepEqual(gotMap, want) {
		t.Errorf("round trip mismatch:\n got %#v\nwant %#v", gotMap, want)
	}

	// A filtered read returns only the requested property.
	sub, err := s.GetFolderProperties(fid, mapi.MakeTag(0x3001, mapi.PtUnicode))
	if err != nil {
		t.Fatal(err)
	}
	if len(sub) != 1 || sub[0].Value != "Inbox" {
		t.Errorf("filtered get = %#v, want single Inbox", sub)
	}
}

func TestSetPropertiesUpserts(t *testing.T) {
	s := openTemp(t)
	fid, err := s.CreateFolder(nil, "F")
	if err != nil {
		t.Fatal(err)
	}
	tag := mapi.MakeTag(0x3001, mapi.PtUnicode)
	if err := s.SetFolderProperties(fid, mapi.PropertyValues{{Tag: tag, Value: "first"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFolderProperties(fid, mapi.PropertyValues{{Tag: tag, Value: "second"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFolderProperties(fid, tag)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "second" {
		t.Errorf("upsert result = %#v, want single 'second'", got)
	}
}
