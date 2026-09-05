package objectstore

import (
	"testing"

	"hermex/internal/mapi"
)

// TestAFreshMailboxHasEveryDefaultFolder is the baseline the backfill restores
// an older mailbox to.
func TestAFreshMailboxHasEveryDefaultFolder(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, fid := range []uint64{
		mapi.PrivateFIDArchive,
		mapi.PrivateFIDConversationHistory,
		mapi.PrivateFIDRecipientCache,
	} {
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		if _, err := st.GetFolderProperties(int64(fid), mapi.PrDisplayName); err != nil {
			t.Errorf("folder %#x is missing from a fresh mailbox: %v", fid, err)
		}
	}
}

// TestAnOlderMailboxGainsTheMissingFolders is the backfill itself. A mailbox
// provisioned before a folder joined the default set holds no row for it, and a
// client asking for that folder by name would be told the mailbox has none.
func TestAnOlderMailboxGainsTheMissingFolders(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Remove the three folders to make the store look like one provisioned
	// before they existed.
	for _, fid := range []uint64{
		mapi.PrivateFIDArchive,
		mapi.PrivateFIDConversationHistory,
		mapi.PrivateFIDRecipientCache,
	} {
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		if _, err := st.objdb.Exec(`DELETE FROM folders WHERE folder_id = ?`, int64(fid)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening is what an ordinary daemon does, and it is where the backfill runs.
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, c := range []struct {
		fid  uint64
		name string
	}{
		{mapi.PrivateFIDArchive, "Archive"},
		{mapi.PrivateFIDConversationHistory, "Conversation History"},
		{mapi.PrivateFIDRecipientCache, "Recipient Cache"},
	} {
		// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
		props, err := st.GetFolderProperties(int64(c.fid), mapi.PrDisplayName)
		if err != nil {
			t.Errorf("folder %#x was not restored: %v", c.fid, err)
			continue
		}
		v, _ := props.Get(mapi.PrDisplayName)
		if name, _ := v.(string); name != c.name {
			t.Errorf("folder %#x = %q, want %q", c.fid, name, c.name)
		}
	}
}

// countFolders reports how many listed folders carry a display name.
func countFolders(t *testing.T, st *Store, name string) int {
	t.Helper()
	folders, err := st.ListFolders()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range folders {
		if f.DisplayName == name {
			n++
		}
	}
	return n
}

// TestTheBackfillKeepsAUserFolderOfTheSameName is the upgrade case that would
// otherwise cost the user their mail: a mailbox where somebody already made
// their own "Archive" must not gain a second folder under that name, because
// every name-based lookup would then pick one of two at random.
func TestTheBackfillKeepsAUserFolderOfTheSameName(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	if _, err := st.objdb.Exec(`DELETE FROM folders WHERE folder_id = ?`, int64(mapi.PrivateFIDArchive)); err != nil {
		t.Fatal(err)
	}
	// The user's own folder, under the same parent and the same name.
	// #nosec G115 -- a store id crosses SQLite's signed 64-bit column; both widths hold the same bits and the value round-trips exactly
	parent := int64(mapi.PrivateFIDIPMSubtree)
	mine, err := st.CreateFolder(&parent, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if n := countFolders(t, st, "Archive"); n != 1 {
		t.Errorf("the mailbox holds %d folders named Archive, want the user's one", n)
	}
	if _, err := st.GetFolderProperties(mine, mapi.PrDisplayName); err != nil {
		t.Errorf("the user's own folder was disturbed: %v", err)
	}
}

// TestTheBackfillLeavesAWholeMailboxAlone keeps the ordinary case free of
// writes: every mailbox opens through this path, so a backfill that rewrote
// folders it found would churn every store on every open.
func TestTheBackfillLeavesAWholeMailboxAlone(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.GetFolderProperties(int64(mapi.PrivateFIDInbox), mapi.PrChangeKey)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	after, err := st.GetFolderProperties(int64(mapi.PrivateFIDInbox), mapi.PrChangeKey)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := before.Get(mapi.PrChangeKey)
	a, _ := after.Get(mapi.PrChangeKey)
	bs, _ := b.([]byte)
	as, _ := a.([]byte)
	if string(bs) != string(as) {
		t.Error("reopening a complete mailbox rewrote a folder that was already there")
	}
}
