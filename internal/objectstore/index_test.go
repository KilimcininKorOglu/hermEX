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

// TestIndexMessage indexes two delivered messages and verifies the index row's
// flag columns, envelope projections, monotonic UID/idx allocation, the
// id-to-mid mapping, and that the index folder mirrors the object-store folder.
func TestIndexMessage(t *testing.T) {
	s := openSeededStore(t)

	build := func(subject string) *oxcmail.Message {
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

	received := time.Unix(1700000000, 0)

	// First message: index it; the first UID in a fresh folder is 1.
	m1 := build("ilk konu")
	eid1, err := s.CreateMessage(mapi.PrivateFIDInbox, m1)
	if err != nil {
		t.Fatal(err)
	}
	uid1, err := s.indexMessage(mapi.PrivateFIDInbox, eid1, midString(uint64(eid1)), m1, 4096, received, FlagSeen|FlagFlagged)
	if err != nil {
		t.Fatal(err)
	}
	if uid1 != 1 {
		t.Errorf("first uid = %d, want 1", uid1)
	}

	var (
		idx, uid, sz, recv                                         int64
		read, flagged, replied, forwarded, deleted, unsent, recent int
		subject, sender, rcpt, mid                                 string
	)
	if err := s.idxdb.QueryRow(
		`SELECT idx, uid, size, received, read, flagged, replied, forwarded, deleted, unsent, recent, subject, sender, rcpt, mid_string
		 FROM messages WHERE message_id=?`, eid1).
		Scan(&idx, &uid, &sz, &recv, &read, &flagged, &replied, &forwarded, &deleted, &unsent, &recent, &subject, &sender, &rcpt, &mid); err != nil {
		t.Fatal(err)
	}
	if idx != 1 || uid != 1 {
		t.Errorf("idx=%d uid=%d, want 1/1", idx, uid)
	}
	if read != 1 || flagged != 1 {
		t.Errorf("read=%d flagged=%d, want 1/1 from FlagSeen|FlagFlagged", read, flagged)
	}
	if replied != 0 || deleted != 0 || unsent != 0 || forwarded != 0 {
		t.Errorf("unset flags wrong: replied=%d deleted=%d unsent=%d forwarded=%d", replied, deleted, unsent, forwarded)
	}
	if recent != 1 {
		t.Errorf("recent=%d, want 1 for a freshly indexed message", recent)
	}
	if sz != 4096 || recv != received.Unix() {
		t.Errorf("size=%d received=%d, want 4096/%d", sz, recv, received.Unix())
	}
	if subject != "ilk konu" {
		t.Errorf("subject projection = %q", subject)
	}
	if sender != "Gönderen Kişi <gonderen@example.test>" {
		t.Errorf("sender projection = %q", sender)
	}
	if rcpt != "Alıcı <alici@example.test>" {
		t.Errorf("rcpt projection = %q", rcpt)
	}
	if mid != midString(uint64(eid1)) {
		t.Errorf("mid_string = %q, want %q", mid, midString(uint64(eid1)))
	}

	// The id-to-mid mapping row was written.
	var mapMid string
	if err := s.idxdb.QueryRow(`SELECT mid_string FROM mapping WHERE message_id=?`, eid1).Scan(&mapMid); err != nil {
		t.Fatal(err)
	}
	if mapMid != midString(uint64(eid1)) {
		t.Errorf("mapping mid_string = %q, want %q", mapMid, midString(uint64(eid1)))
	}

	// Second message: UID and idx advance monotonically.
	m2 := build("ikinci konu")
	eid2, err := s.CreateMessage(mapi.PrivateFIDInbox, m2)
	if err != nil {
		t.Fatal(err)
	}
	uid2, err := s.indexMessage(mapi.PrivateFIDInbox, eid2, midString(uint64(eid2)), m2, 2048, received, 0)
	if err != nil {
		t.Fatal(err)
	}
	if uid2 != 2 {
		t.Errorf("second uid = %d, want 2", uid2)
	}

	// The index folder mirrors the object-store folder; uidnext advanced past
	// both allocations.
	var name string
	var uidnext int64
	if err := s.idxdb.QueryRow(
		`SELECT name, uidnext FROM folders WHERE folder_id=?`, mapi.PrivateFIDInbox).Scan(&name, &uidnext); err != nil {
		t.Fatal(err)
	}
	if uidnext != 3 {
		t.Errorf("uidnext = %d, want 3 after two allocations", uidnext)
	}
	if want := folderDisplayName(t, s, mapi.PrivateFIDInbox); name != want {
		t.Errorf("index folder name = %q, want %q", name, want)
	}
}
