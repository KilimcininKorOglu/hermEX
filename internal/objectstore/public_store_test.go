package objectstore

import (
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

	// The four structural folders carry the public-store names and parents,
	// faithful to the reference public store. A private mailbox's 0x09 node is
	// instead "Top of Information Store", so these names prove the public seed ran.
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
		if err != nil {
			t.Fatalf("read folder %#x: %v", want.fid, err)
		}
		if got, _ := stringProp(props, mapi.PrDisplayName); got != want.name {
			t.Errorf("folder %#x name = %q, want %q", want.fid, got, want.name)
		}
	}

	// It is NOT a private mailbox: the private Inbox id holds no folder here.
	props, err := s.GetFolderProperties(int64(mapi.PrivateFIDInbox), mapi.PrDisplayName)
	if err != nil {
		t.Fatalf("probe private inbox: %v", err)
	}
	if got, ok := stringProp(props, mapi.PrDisplayName); ok {
		t.Errorf("public store has a private Inbox folder named %q; seed used the wrong hierarchy", got)
	}

	// CreateFolder roots at the PUBLIC IPM subtree (0x02), not the private one
	// (0x09): the new folder takes a user-range id and ListFolders — which walks
	// the public subtree — enumerates exactly it. Wrong rooting would leave
	// ListFolders empty.
	annFID, err := s.CreateFolder(nil, "Announcements")
	if err != nil {
		t.Fatal(err)
	}
	if annFID < int64(mapi.PublicFIDUnassignedStart) {
		t.Errorf("new folder id = %#x, want >= %#x (user range)", annFID, mapi.PublicFIDUnassignedStart)
	}
	folders, err := s.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].DisplayName != "Announcements" || folders[0].ParentID != nil {
		t.Fatalf("ListFolders = %+v, want exactly [Announcements] directly under the public IPM subtree", folders)
	}

	// Content can be posted to a public folder — the capability every public-folder
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
	info, err := s.AppendMessage(annFID, raw, time.Unix(1700043200, 0), 0)
	if err != nil {
		t.Fatalf("append to public folder: %v", err)
	}
	if info.UID != 1 || info.Size <= 0 {
		t.Errorf("appended message info = %+v, want uid 1 and size > 0", info)
	}
}
