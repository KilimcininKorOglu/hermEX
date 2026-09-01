package objectstore

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestGetMessageRaw serves a delivered message from the eml cache, then forces
// a cache miss with a stale index size and verifies regeneration re-synthesizes
// the wire form, re-caches it, and corrects the index size to the served bytes
// (the RFC822.SIZE invariant). Unknown UIDs report ErrNotFound.
func TestGetMessageRaw(t *testing.T) {
	s := openSeededStore(t)

	raw := []byte(strings.Join([]string{
		"From: a@example.test",
		"To: b@example.test",
		"Subject: konu",
		"Date: Wed, 15 Nov 2023 10:13:20 +0000",
		"",
		"gövde metni burada.",
		"",
	}, "\r\n"))
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}

	// Served from cache: the bytes match the reported size.
	got, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(got)) != info.Size {
		t.Errorf("served %d bytes, reported size %d", len(got), info.Size)
	}

	// Force a stale index size and drop the cache, then re-fetch: regeneration
	// rewrites the cache and corrects the size to the served bytes.
	mid := midString(uint64(info.ID))
	if _, err := s.idxdb.Exec(`UPDATE messages SET size=999999 WHERE message_id=?`, info.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.emlPath(mid)); err != nil {
		t.Fatal(err)
	}
	regen, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(regen) == 0 {
		t.Fatal("regenerated eml is empty")
	}
	if _, err := os.Stat(s.emlPath(mid)); err != nil {
		t.Errorf("eml was not re-cached: %v", err)
	}
	var idxSize int64
	if err := s.idxdb.QueryRow(`SELECT size FROM messages WHERE message_id=?`, info.ID).Scan(&idxSize); err != nil {
		t.Fatal(err)
	}
	if idxSize != int64(len(regen)) {
		t.Errorf("index size %d != regenerated served bytes %d (stale size not corrected)", idxSize, len(regen))
	}

	// The regenerated form still carries the same body.
	served, err := oxcmail.Import(regen, oxcmail.Options{Resolver: s.GetNamedPropIDs})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := asMap(served.Props)[mapi.PrBody].(string); !strings.Contains(b, "gövde metni") {
		t.Errorf("regenerated body lost its content: %q", b)
	}

	// A second fetch is served from the rewritten cache and is byte-identical.
	again, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, regen) {
		t.Error("second fetch differs from the re-cached eml")
	}

	// Unknown UID reports ErrNotFound.
	if _, err := s.GetMessageRaw(mapi.PrivateFIDInbox, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMessageRaw(missing) err = %v, want ErrNotFound", err)
	}
}

// TestEditingPrunedMessageKeepsSizeConsistent proves an in-place edit of a mail
// message whose eml cache was pruned still corrects the index RFC822 size, so the
// size reported before any body fetch equals the bytes that fetch returns. Before
// the fix refreshEML skipped the size update whenever the cache file was absent, so
// a pruned-then-edited message reported its pre-edit size until the next raw read.
func TestEditingPrunedMessageKeepsSizeConsistent(t *testing.T) {
	s := openSeededStore(t)
	raw := []byte(strings.Join([]string{
		"From: a@x.test", "To: b@x.test", "Subject: short",
		"Date: Wed, 15 Nov 2023 10:13:20 +0000", "", "body", "",
	}, "\r\n"))
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Warm the cache so the index size is set, then simulate PruneEMLCache: the index
	// row and its size stay, only the file is removed.
	if _, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.emlPath(midString(uint64(info.ID)))); err != nil {
		t.Fatal(err)
	}

	// An in-place edit that changes the exported length (Importance adds a header).
	// It triggers refreshEML, which must correct the index size even with no cache.
	if err := s.SetMessageProperties(info.ID, mapi.PropertyValues{
		{Tag: mapi.PrImportance, Value: int32(2)},
	}); err != nil {
		t.Fatal(err)
	}

	// Capture the recorded RFC822 size BEFORE any raw fetch (a fetch would itself
	// re-sync it, masking the defect).
	var got int64
	if err := s.idxdb.QueryRow(`SELECT size FROM messages WHERE message_id=?`, info.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	body, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if got != int64(len(body)) {
		t.Errorf("recorded RFC822.SIZE %d != served body %d after editing a pruned message", got, len(body))
	}
}
