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
