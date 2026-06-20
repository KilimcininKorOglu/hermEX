package oxews

import (
	"encoding/base64"
	"fmt"
	"testing"
)

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
	got, _, err := DecodeFolderID(EncodeFolderID(0x1e))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != 0x1e {
		t.Errorf("round trip = %d, want 30", got)
	}
}

// TestItemIDMailboxRoundTrip confirms an item id carrying a target mailbox (whose SMTP
// itself contains dots) round-trips losslessly.
func TestItemIDMailboxRoundTrip(t *testing.T) {
	in := ItemID{FolderID: 13, MessageID: 0x1000a, UID: 7, Mailbox: "boss@hermex.test"}
	got, err := DecodeItemID(EncodeItemID(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != in {
		t.Errorf("round trip = %+v, want %+v", got, in)
	}
}

// TestItemIDLegacyTokenDecodes confirms a token minted before the Mailbox field (the
// bare three-field form) still decodes, to an own-mailbox id.
func TestItemIDLegacyTokenDecodes(t *testing.T) {
	legacy := base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d.%d.%d", 13, 0x1000a, 7))
	got, err := DecodeItemID(legacy)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := ItemID{FolderID: 13, MessageID: 0x1000a, UID: 7}
	if got != want {
		t.Errorf("legacy decode = %+v, want %+v (empty Mailbox)", got, want)
	}
}

// TestFolderIDMailboxRoundTrip confirms a mailbox-aware folder id round-trips and that
// the own-mailbox form is byte-identical to the legacy encoding.
func TestFolderIDMailboxRoundTrip(t *testing.T) {
	fid, mb, err := DecodeFolderID(EncodeFolderIDFor(0x1e, "boss@hermex.test"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if fid != 0x1e || mb != "boss@hermex.test" {
		t.Errorf("round trip = %d/%q, want 30/boss@hermex.test", fid, mb)
	}
	if EncodeFolderIDFor(0x1e, "") != EncodeFolderID(0x1e) {
		t.Errorf("own-mailbox folder id must equal the legacy form")
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
