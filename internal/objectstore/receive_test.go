package objectstore

import (
	"errors"
	"testing"

	"hermex/internal/mapi"
)

// TestReceiveFolder exercises the receive-folder resolution: the seeded defaults,
// longest-prefix matching down the dotted class, the empty-class catch-all, a
// custom mapping (set, resolved through a sub-class, then removed), the
// non-existent-folder rejection, and the table enumeration.
func TestReceiveFolder(t *testing.T) {
	s := openSeededStore(t)
	inbox := int64(mapi.PrivateFIDInbox)
	root := int64(mapi.PrivateFIDRoot)

	check := func(class string, wantFID int64, wantExplicit string) {
		t.Helper()
		fid, explicit, err := s.GetReceiveFolder(class)
		if err != nil {
			t.Fatalf("GetReceiveFolder(%q): %v", class, err)
		}
		if fid != wantFID || explicit != wantExplicit {
			t.Errorf("GetReceiveFolder(%q) = (%d, %q), want (%d, %q)", class, fid, explicit, wantFID, wantExplicit)
		}
	}

	// Seeded defaults and prefix matching.
	check("", inbox, "")
	check("IPM", inbox, "IPM")
	check("IPC", root, "IPC")
	check("IPM.Note.Foo", inbox, "IPM") // longest existing prefix is the seeded "IPM"
	check("XYZ.Unknown", inbox, "")     // no prefix matches → empty default → Inbox

	// A custom mapping is resolved through a longer sub-class.
	sent := int64(mapi.PrivateFIDSentItems)
	if err := s.SetReceiveFolder("IPM.Note.Custom", sent); err != nil {
		t.Fatalf("SetReceiveFolder: %v", err)
	}
	check("IPM.Note.Custom.Sub", sent, "IPM.Note.Custom")
	check("IPM.Note.Custom", sent, "IPM.Note.Custom")

	// Removing it falls back to the seeded IPM prefix.
	if err := s.SetReceiveFolder("IPM.Note.Custom", 0); err != nil {
		t.Fatalf("SetReceiveFolder(remove): %v", err)
	}
	check("IPM.Note.Custom", inbox, "IPM")

	// A non-existent target folder is rejected.
	if err := s.SetReceiveFolder("IPM.Note.X", 0x7FFFFF); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetReceiveFolder(non-existent folder) = %v, want ErrNotFound", err)
	}

	// The table lists exactly the seeded rows (the custom mapping was removed).
	tbl, err := s.ReceiveFolderTable()
	if err != nil {
		t.Fatalf("ReceiveFolderTable: %v", err)
	}
	if len(tbl) != 4 {
		t.Errorf("ReceiveFolderTable len = %d, want 4 (seeded defaults)", len(tbl))
	}
}
