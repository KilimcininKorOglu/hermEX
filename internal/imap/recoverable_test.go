package imap

import (
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestIMAPExpungeGoesToDumpster proves an IMAP EXPUNGE soft-deletes the \Deleted
// message into the Recoverable Items dumpster rather than purging it: it leaves the
// mailbox but stays recoverable. This is the IMAP leg of routing every hard-delete
// into the dumpster.
func TestIMAPExpungeGoesToDumpster(t *testing.T) {
	c, path := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "SELECT INBOX")
	c.mustOK("a3", `STORE 1 +FLAGS (\Deleted)`)
	c.mustOK("a4", "EXPUNGE")

	// A second handle (WAL allows concurrent reads) sees the expunged message in the
	// dumpster, not gone.
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dump, err := st.ListSoftDeleted(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	if len(dump) != 1 {
		t.Fatalf("dumpster has %d items after EXPUNGE, want 1 (recoverable)", len(dump))
	}
}
