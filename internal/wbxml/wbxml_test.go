package wbxml

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// header is the fixed four-field ActiveSync WBXML prologue: version 1.3,
// public id 1, charset 106 (UTF-8), empty string table.
var header = []byte{0x03, 0x01, 0x6A, 0x00}

// The vectors below are authored BY HAND from the MS-ASWBXML framing rules
// (the internal spec §3-§4), not produced by this package's encoder — so they are
// an independent oracle. A symmetric encode/decode bug would still satisfy a
// pure round-trip test; it cannot satisfy these fixed bytes.

// TestHeaderOnly pins the bare header a nil document encodes to.
func TestHeaderOnly(t *testing.T) {
	if got := Marshal(nil); !bytes.Equal(got, header) {
		t.Errorf("Marshal(nil) = % x, want % x", got, header)
	}
}

// TestFolderSyncVector pins <FolderSync><SyncKey>0</SyncKey></FolderSync>:
// SWITCH_PAGE to FolderHierarchy (7), FolderSync (0x16|content), SyncKey
// (0x12|content), STR_I "0", and two ENDs.
func TestFolderSyncVector(t *testing.T) {
	want := append(append([]byte{}, header...),
		0x00, 0x07, // SWITCH_PAGE 7
		0x56,             // FolderSync | content
		0x52,             // SyncKey | content
		0x03, 0x30, 0x00, // STR_I "0"
		0x01, // END SyncKey
		0x01, // END FolderSync
	)
	tree := Elem(FHFolderSync, Str(FHSyncKey, "0"))

	if got := Marshal(tree); !bytes.Equal(got, want) {
		t.Errorf("Marshal = % x\nwant     % x", got, want)
	}
	got, err := Unmarshal(want)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, tree) {
		t.Errorf("Unmarshal = %#v\nwant      %#v", got, tree)
	}
}

// TestEmptyElementVector pins a self-closing child: <Sync><GetChanges/></Sync>.
// GetChanges (0x13) is emitted as a bare token with no content bit and no END.
func TestEmptyElementVector(t *testing.T) {
	want := append(append([]byte{}, header...),
		0x45, // Sync | content
		0x13, // GetChanges (no content, self-closing)
		0x01, // END Sync
	)
	tree := Elem(ASSync, Empty(ASGetChanges))

	if got := Marshal(tree); !bytes.Equal(got, want) {
		t.Errorf("Marshal = % x, want % x", got, want)
	}
	got, err := Unmarshal(want)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, tree) {
		t.Errorf("Unmarshal mismatch:\n got %#v\nwant %#v", got, tree)
	}
}

// TestOpaqueVector pins opaque framing carrying an embedded NUL, which STR_I
// could not: <Body><Data>41 00 42</Data></Body> on AirSyncBase (page 0x11).
func TestOpaqueVector(t *testing.T) {
	want := append(append([]byte{}, header...),
		0x00, 0x11, // SWITCH_PAGE AirSyncBase
		0x4A,                         // Body | content
		0x4B,                         // Data | content
		0xC3, 0x03, 0x41, 0x00, 0x42, // OPAQUE len 3, bytes 41 00 42
		0x01, // END Data
		0x01, // END Body
	)
	tree := Elem(ABBody, Opaque(ABData, []byte{0x41, 0x00, 0x42}))

	if got := Marshal(tree); !bytes.Equal(got, want) {
		t.Errorf("Marshal = % x, want % x", got, want)
	}
	got, err := Unmarshal(want)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !bytes.Equal(got.Children[0].Opaque, []byte{0x41, 0x00, 0x42}) {
		t.Errorf("opaque round-trip dropped the embedded NUL: % x", got.Children[0].Opaque)
	}
}

// TestMBUint pins the multi-byte integer encoding at the documented boundaries
// (0, single-byte max, the two-byte case, and a three-byte value).
func TestMBUint(t *testing.T) {
	cases := []struct {
		v    uint32
		want []byte
	}{
		{0, []byte{0x00}},
		{106, []byte{0x6A}},
		{200, []byte{0x81, 0x48}},
		{16384, []byte{0x81, 0x80, 0x00}},
	}
	for _, c := range cases {
		w := &writer{}
		w.mbUint(c.v)
		if !bytes.Equal(w.buf, c.want) {
			t.Errorf("mbUint(%d) = % x, want % x", c.v, w.buf, c.want)
		}
		r := &reader{buf: c.want}
		got, err := r.mbUint()
		if err != nil {
			t.Fatalf("mbUint decode %d: %v", c.v, err)
		}
		if got != c.v {
			t.Errorf("mbUint decode % x = %d, want %d", c.want, got, c.v)
		}
	}
}

// TestProvisionPolicyDocVector pins the EASProvisionDoc policy-detail token values
// against hand-authored bytes ([MS-ASWBXML] code page 14): a wrong constant would emit
// a token a device rejects, so this is the byte-exact oracle for the new policy tokens.
func TestProvisionPolicyDocVector(t *testing.T) {
	want := append(append([]byte{}, header...),
		0x00, 0x0E, // SWITCH_PAGE 14 (Provision)
		0x45,                        // Provision | content
		0x46,                        // Policies | content
		0x47,                        // Policy | content
		0x4A,                        // Data | content
		0x4D,                        // EASProvisionDoc | content
		0x4E, 0x03, '1', 0x00, 0x01, // DevicePasswordEnabled "1"
		0x54, 0x03, '6', 0x00, 0x01, // MinDevicePasswordLength "6"
		0x55, 0x03, '9', '0', '0', 0x00, 0x01, // MaxInactivityTimeDeviceLock "900"
		0x5D, 0x03, '1', 0x00, 0x01, // RequireDeviceEncryption "1"
		0x01, // END EASProvisionDoc
		0x01, // END Data
		0x01, // END Policy
		0x01, // END Policies
		0x01, // END Provision
	)
	tree := Elem(PVProvision,
		Elem(PVPolicies,
			Elem(PVPolicy,
				Elem(PVData,
					Elem(PVEASProvisionDoc,
						Str(PVDevicePasswordEnabled, "1"),
						Str(PVMinDevicePasswordLength, "6"),
						Str(PVMaxInactivityTimeDeviceLock, "900"),
						Str(PVRequireDeviceEncryption, "1"),
					)))))

	if got := Marshal(tree); !bytes.Equal(got, want) {
		t.Errorf("Marshal = % x\nwant     % x", got, want)
	}
	got, err := Unmarshal(want)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, tree) {
		t.Errorf("Unmarshal = %#v\nwant      %#v", got, tree)
	}
}

// TestRoundTripMultiPage exercises SWITCH_PAGE in both directions (AirSync →
// Email → AirSyncBase → back) plus opaque content, then checks the tree
// survives encode+decode unchanged.
func TestRoundTripMultiPage(t *testing.T) {
	tree := Elem(ASSync,
		Elem(ASCollections,
			Elem(ASCollection,
				Str(ASSyncKey, "1"),
				Str(ASCollectionID, "5"),
				Elem(ASCommands,
					Elem(ASAdd,
						Str(ASServerID, "7"),
						Elem(ASData,
							Str(EMSubject, "Hi"),
							Elem(ABBody,
								Str(ABType, "1"),
								Opaque(ABData, []byte("Body\x00bytes")),
							),
						),
					),
				),
			),
		),
	)
	got, err := Unmarshal(Marshal(tree))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, tree) {
		t.Fatalf("round-trip mismatch:\n got %#v\nwant %#v", got, tree)
	}
	// Exercise the navigation helpers on the decoded tree.
	coll := got.Child(ASCollections).Child(ASCollection)
	if coll == nil {
		t.Fatal("Child navigation lost the Collection node")
	}
	if key := coll.ChildText(ASSyncKey); key != "1" {
		t.Errorf("ChildText(ASSyncKey) = %q, want \"1\"", key)
	}
}

// TestRoundTripCalendar exercises the Calendar code page (0x04, MS-ASCAL): a Sync
// ApplicationData carrying an appointment's fields switches AirSync → Calendar and
// back, with a nested Recurrence subtree, and survives encode+decode unchanged.
func TestRoundTripCalendar(t *testing.T) {
	tree := Elem(ASSync,
		Elem(ASCollection,
			Elem(ASCommands,
				Elem(ASAdd,
					Str(ASServerID, "3"),
					Elem(ASData,
						Str(CalSubject, "Standup"),
						Str(CalStartTime, "20260619T090000Z"),
						Str(CalEndTime, "20260619T093000Z"),
						Str(CalUID, "abc-123"),
						Str(CalBusyStatus, "2"),
						Str(CalAllDayEvent, "0"),
						Elem(CalRecurrence,
							Str(CalType, "1"),
							Str(CalInterval, "1"),
							Str(CalDayOfWeek, "16"),
						),
					),
				),
			),
		),
	)
	got, err := Unmarshal(Marshal(tree))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, tree) {
		t.Fatalf("calendar round-trip mismatch:\n got %#v\nwant %#v", got, tree)
	}
	// The appointment keeps its Calendar-page fields after the page switch back.
	data := got.Child(ASCollection).Child(ASCommands).Child(ASAdd).Child(ASData)
	if data == nil {
		t.Fatal("navigation lost the ApplicationData node")
	}
	if st := data.ChildText(CalStartTime); st != "20260619T090000Z" {
		t.Errorf("CalStartTime = %q, want the round-tripped value", st)
	}
	if rec := data.Child(CalRecurrence); rec == nil || rec.ChildText(CalType) != "1" {
		t.Errorf("Recurrence subtree lost across the page switch: %#v", rec)
	}
}

// TestRoundTripMeetingResponse exercises the MeetingResponse code page (0x08): the
// first MeetingResponse tag switches AirSync -> MeetingResponse, and a Result tree
// survives encode+decode unchanged.
func TestRoundTripMeetingResponse(t *testing.T) {
	tree := Elem(MRMeetingResponse,
		Elem(MRResult,
			Str(MRRequestID, "42"),
			Str(MRStatus, "1"),
			Str(MRCalendarID, "7"),
		),
	)
	got, err := Unmarshal(Marshal(tree))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, tree) {
		t.Fatalf("meeting-response round-trip mismatch:\n got %#v\nwant %#v", got, tree)
	}
	res := got.Child(MRResult)
	if res == nil || res.ChildText(MRStatus) != "1" || res.ChildText(MRCalendarID) != "7" {
		t.Errorf("Result subtree lost across the page switch: %#v", res)
	}
}

// TestRejectAttributes confirms a tag carrying the attribute bit (0x80) is
// rejected: ActiveSync never uses WBXML attributes.
func TestRejectAttributes(t *testing.T) {
	b := append(append([]byte{}, header...), 0x85) // token 5 with attribute bit
	if _, err := Unmarshal(b); !errors.Is(err, ErrFormat) {
		t.Errorf("err = %v, want ErrFormat", err)
	}
}

// TestBadHeader rejects a wrong WBXML version and a wrong charset.
func TestBadHeader(t *testing.T) {
	if _, err := Unmarshal([]byte{0x02, 0x01, 0x6A, 0x00, 0x45, 0x01}); !errors.Is(err, ErrFormat) {
		t.Errorf("bad version: err = %v, want ErrFormat", err)
	}
	if _, err := Unmarshal([]byte{0x03, 0x01, 0x6B, 0x00, 0x45, 0x01}); !errors.Is(err, ErrFormat) {
		t.Errorf("bad charset: err = %v, want ErrFormat", err)
	}
}

// TestMBUintOverflow rejects a multi-byte integer that never terminates within
// five bytes.
func TestMBUintOverflow(t *testing.T) {
	b := []byte{0x03, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00} // publicID never ends
	if _, err := Unmarshal(b); !errors.Is(err, ErrFormat) {
		t.Errorf("err = %v, want ErrFormat", err)
	}
}

// TestTruncated rejects a document whose final END is missing.
func TestTruncated(t *testing.T) {
	full := Marshal(Elem(FHFolderSync, Str(FHSyncKey, "0")))
	if _, err := Unmarshal(full[:len(full)-1]); !errors.Is(err, ErrUnderflow) {
		t.Errorf("err = %v, want ErrUnderflow", err)
	}
}

// TestUnterminatedString rejects an STR_I with no NUL terminator before EOF.
func TestUnterminatedString(t *testing.T) {
	b := append(append([]byte{}, header...), 0x45, 0x03, 'a', 'b') // Sync, STR_I "ab" (no NUL)
	if _, err := Unmarshal(b); !errors.Is(err, ErrUnderflow) {
		t.Errorf("err = %v, want ErrUnderflow", err)
	}
}
