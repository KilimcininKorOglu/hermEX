package nspi

import (
	"bytes"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestGetPropsThumbnailPhoto proves the address book serves a GAL entry's
// portrait (PR_EMS_AB_THUMBNAIL_PHOTO) from the mailbox's cross-protocol user-
// photo property when it is explicitly requested.
func TestGetPropsThumbnailPhoto(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	photo := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 1, 2, 3}
	if err := st.SetUserPhoto(photo); err != nil {
		t.Fatalf("set photo: %v", err)
	}
	st.Close()

	s := NewServer(maskedGAL{
		{DisplayName: "alice@hermex.test", Address: "alice@hermex.test", StorePath: dir},
	}, testGUID)
	cols := []mapi.PropTag{mapi.PrEmsAbThumbnailPhoto}
	result, row := decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midBase, codePage: 1252}, cols)))
	if result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	v, ok := row.Get(mapi.PrEmsAbThumbnailPhoto)
	if !ok {
		t.Fatal("PR_EMS_AB_THUMBNAIL_PHOTO not in row")
	}
	got, ok := v.([]byte)
	if !ok || !bytes.Equal(got, photo) {
		t.Errorf("photo = %v, want %v", got, photo)
	}
}

// TestGetPropsThumbnailPhotoAbsent proves a mailbox with no portrait yields the
// PT_ERROR(ecNotFound) marker for the photo, not an empty value.
func TestGetPropsThumbnailPhotoAbsent(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.Close() // no photo set

	s := NewServer(maskedGAL{
		{DisplayName: "alice@hermex.test", Address: "alice@hermex.test", StorePath: dir},
	}, testGUID)
	cols := []mapi.PropTag{mapi.PrEmsAbThumbnailPhoto}
	result, row := decodeGetProps(t, s.GetProps(buildGetProps(stat{curRec: midBase, codePage: 1252}, cols)))
	if result != ecWarnWithErrors {
		t.Fatalf("result = %#x, want ecWarnWithErrors", result)
	}
	if v, _ := row.Get(errorTag(mapi.PrEmsAbThumbnailPhoto)); v != ecNotFound {
		t.Errorf("absent photo marker = %v, want ecNotFound", v)
	}
}
