package objectstore

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestOpenPublicStoreSeedsHierarchy proves OpenPublic provisions the public-folder
// hierarchy (not a private mailbox) and that the folder API roots at the public
// IPM subtree, so an administrator's folders land where the EWS/IMAP/webmail
// public-folder surfaces look for them. Each structural name, the absence of a
// private Inbox, and the rooting are the load-bearing facts those surfaces depend
// on.
func TestOpenPublicStoreSeedsHierarchy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "domainpub")
	s, err := OpenPublic(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	wantPublicStructuralNames(t, s)

	// It is NOT a private mailbox: the private Inbox id holds no folder here.
	props, err := s.GetFolderProperties(int64(mapi.PrivateFIDInbox), mapi.PrDisplayName)
	mustNoErr(t, "probe private inbox", err)
	if got, ok := stringProp(props, mapi.PrDisplayName); ok {
		t.Errorf("public store has a private Inbox folder named %q; seed used the wrong hierarchy", got)
	}

	// CreateFolder roots at the PUBLIC IPM subtree (0x02), not the private one
	// (0x09): the new folder takes a user-range id and ListFolders, which walks
	// the public subtree, enumerates exactly it. Wrong rooting would leave
	// ListFolders empty.
	annFID := mustCreateFolder(t, s, nil, "Announcements")
	if annFID < int64(mapi.PublicFIDUnassignedStart) {
		t.Errorf("new folder id = %#x, want >= %#x (user range)", annFID, mapi.PublicFIDUnassignedStart)
	}
	folders, err := s.ListFolders()
	mustNoErr(t, "list folders", err)
	if len(folders) != 1 || folders[0].DisplayName != "Announcements" || folders[0].ParentID != nil {
		t.Fatalf("ListFolders = %+v, want exactly [Announcements] directly under the public IPM subtree", folders)
	}

	// Content can be posted to a public folder, the capability every public-folder
	// surface (IMAP APPEND, admin) ultimately writes through.
	raw := []byte(strings.Join([]string{
		"From: poster@local.test",
		"Subject: announcement",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"hello public folder",
		"",
	}, "\r\n"))
	info := mustAppendMessage(t, s, annFID, raw, time.Unix(1700043200, 0), 0)
	wantEq(t, "appended message uid", info.UID, uint32(1))
	if info.Size <= 0 {
		t.Errorf("appended message size = %d, want > 0", info.Size)
	}
}

// wantPublicStructuralNames checks the four structural folders carry the
// public-store names. A private mailbox's 0x09 node is instead "Top of
// Information Store", so these names prove the public seed ran.
func wantPublicStructuralNames(t *testing.T, s *Store) {
	t.Helper()
	for _, want := range []struct {
		fid  int
		name string
	}{
		{mapi.PublicFIDRoot, "Root Container"},
		{mapi.PublicFIDIPMSubtree, "IPM_SUBTREE"},
		{mapi.PublicFIDNonIPMSubtree, "NON_IPM_SUBTREE"},
		{mapi.PublicFIDEFormsRegistry, "EFORMS REGISTRY"},
	} {
		props, err := s.GetFolderProperties(int64(want.fid), mapi.PrDisplayName)
		mustNoErr(t, fmt.Sprintf("read folder %#x", want.fid), err)
		got, _ := stringProp(props, mapi.PrDisplayName)
		wantEq(t, fmt.Sprintf("folder %#x name", want.fid), got, want.name)
	}
}
