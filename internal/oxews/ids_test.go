package oxews

import "testing"

// TestItemIDRoundTrip confirms an item id encodes and decodes losslessly.
func TestItemIDRoundTrip(t *testing.T) {
	in := ItemID{FolderID: 13, MessageID: 0x1000a, UID: 7}
	got, err := DecodeItemID(EncodeItemID(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != in {
		t.Errorf("round trip = %+v, want %+v", got, in)
	}
}

// TestFolderIDRoundTrip confirms a folder id encodes and decodes losslessly.
func TestFolderIDRoundTrip(t *testing.T) {
	got, err := DecodeFolderID(EncodeFolderID(0x1e))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != 0x1e {
		t.Errorf("round trip = %d, want 30", got)
	}
}

// TestAttachmentIDRoundTrip confirms an attachment id encodes and decodes
// losslessly.
func TestAttachmentIDRoundTrip(t *testing.T) {
	mid, idx, err := DecodeAttachmentID(EncodeAttachmentID(0x20001, 3))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mid != 0x20001 || idx != 3 {
		t.Errorf("round trip = (%d, %d), want (131073, 3)", mid, idx)
	}
}

// TestDecodeBadID confirms malformed ids are rejected.
func TestDecodeBadID(t *testing.T) {
	if _, err := DecodeItemID("not-base64!!!"); err == nil {
		t.Error("expected error for malformed item id")
	}
	if _, _, err := DecodeAttachmentID("@@@"); err == nil {
		t.Error("expected error for malformed attachment id")
	}
}
