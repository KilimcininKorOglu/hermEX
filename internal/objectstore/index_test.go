package objectstore

import (
	"fmt"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// folderDisplayName reads a folder's display name the way the index projection
// does, so the test asserts the index name mirrors the real folder name rather
// than re-deriving it.
func folderDisplayName(t *testing.T, s *Store, folderID int64) string {
	t.Helper()
	props, err := s.GetFolderProperties(folderID, mapi.PrDisplayName)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := props.Get(mapi.PrDisplayName); ok {
		if dn, ok := v.(string); ok && dn != "" {
			return dn
		}
	}
	return fmt.Sprintf("folder-%d", folderID)
}

// indexRow is one row of the IMAP index, as the projection writes it.
type indexRow struct {
	idx, uid, size, received                                   int64
	read, flagged, replied, forwarded, deleted, unsent, recent int
	subject, sender, rcpt, mid                                 string
}

// readIndexRow loads the index row an object was projected into.
func readIndexRow(t *testing.T, s *Store, messageID int64) indexRow {
	t.Helper()
	var r indexRow
	mustScan(t, s.idxdb.QueryRow(
		`SELECT idx, uid, size, received, read, flagged, replied, forwarded, deleted, unsent, recent, subject, sender, rcpt, mid_string
		 FROM messages WHERE message_id=?`, messageID),
		&r.idx, &r.uid, &r.size, &r.received, &r.read, &r.flagged, &r.replied, &r.forwarded,
		&r.deleted, &r.unsent, &r.recent, &r.subject, &r.sender, &r.rcpt, &r.mid)
	return r
}

// indexTestMessage builds a message whose envelope fields all project into the
// index.
func indexTestMessage(subject string) *oxcmail.Message {
	return &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrSubject, Value: subject},
			{Tag: mapi.PrSentRepresentingName, Value: "Gönderen Kişi"},
			{Tag: mapi.PrSentRepresentingSmtpAddress, Value: "gonderen@example.test"},
		},
		Recipients: []mapi.PropertyValues{
			{
				{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
				{Tag: mapi.PrDisplayName, Value: "Alıcı"},
				{Tag: mapi.PrSmtpAddress, Value: "alici@example.test"},
			},
		},
	}
}

// wantIndexedRow checks the flag columns and envelope projections of a freshly
// indexed message.
func wantIndexedRow(t *testing.T, r indexRow, received time.Time, messageID int64) {
	t.Helper()
	wantEq(t, "idx", r.idx, int64(1))
	wantEq(t, "uid", r.uid, int64(1))
	wantEq(t, "read (from FlagSeen)", r.read, 1)
	wantEq(t, "flagged (from FlagFlagged)", r.flagged, 1)
	wantEq(t, "replied", r.replied, 0)
	wantEq(t, "forwarded", r.forwarded, 0)
	wantEq(t, "deleted", r.deleted, 0)
	wantEq(t, "unsent", r.unsent, 0)
	wantEq(t, "recent on a freshly indexed message", r.recent, 1)
	wantEq(t, "size", r.size, int64(4096))
	wantEq(t, "received", r.received, received.Unix())
	wantEq(t, "subject projection", r.subject, "ilk konu")
	wantEq(t, "sender projection", r.sender, "Gönderen Kişi <gonderen@example.test>")
	wantEq(t, "rcpt projection", r.rcpt, "Alıcı <alici@example.test>")
	wantEq(t, "mid_string", r.mid, midString(uint64(messageID)))
}

// TestIndexMessage indexes two delivered messages and verifies the index row's
// flag columns, envelope projections, monotonic UID/idx allocation, the
// id-to-mid mapping, and that the index folder mirrors the object-store folder.
func TestIndexMessage(t *testing.T) {
	s := openSeededStore(t)
	received := time.Unix(1700000000, 0)

	// First message: index it; the first UID in a fresh folder is 1.
	m1 := indexTestMessage("ilk konu")
	eid1 := mustCreateMessage(t, s, mapi.PrivateFIDInbox, m1)
	uid1 := mustIndexMessage(t, s, mapi.PrivateFIDInbox, eid1, m1, 4096, received, FlagSeen|FlagFlagged)
	wantEq(t, "first uid", uid1, int64(1))
	wantIndexedRow(t, readIndexRow(t, s, eid1), received, eid1)

	// The id-to-mid mapping row was written.
	var mapMid string
	mustScan(t, s.idxdb.QueryRow(`SELECT mid_string FROM mapping WHERE message_id=?`, eid1), &mapMid)
	wantEq(t, "mapping mid_string", mapMid, midString(uint64(eid1)))

	// Second message: UID and idx advance monotonically.
	m2 := indexTestMessage("ikinci konu")
	eid2 := mustCreateMessage(t, s, mapi.PrivateFIDInbox, m2)
	uid2 := mustIndexMessage(t, s, mapi.PrivateFIDInbox, eid2, m2, 2048, received, 0)
	wantEq(t, "second uid", uid2, int64(2))

	// The index folder mirrors the object-store folder; uidnext advanced past
	// both allocations.
	var name string
	var uidnext int64
	mustScan(t, s.idxdb.QueryRow(`SELECT name, uidnext FROM folders WHERE folder_id=?`, mapi.PrivateFIDInbox), &name, &uidnext)
	wantEq(t, "uidnext after two allocations", uidnext, int64(3))
	wantEq(t, "index folder name", name, folderDisplayName(t, s, mapi.PrivateFIDInbox))
}
