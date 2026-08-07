package objectstore

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// multipartRaw is a message with an attachment, so its wire form carries a MIME
// boundary. The boundary is minted per export, which is what makes two independent
// regenerations of one message observably different.
func multipartRaw() []byte {
	return []byte(strings.Join([]string{
		"From: a@example.test",
		"To: b@example.test",
		"Subject: with an attachment",
		"Date: Wed, 15 Nov 2023 10:13:20 +0000",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="orig-boundary"`,
		"",
		"--orig-boundary",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"body text",
		"",
		"--orig-boundary",
		`Content-Type: text/plain; name="note.txt"`,
		`Content-Disposition: attachment; filename="note.txt"`,
		"",
		"attached text",
		"",
		"--orig-boundary--",
		"",
	}, "\r\n"))
}

// TestConcurrentRegenerationYieldsOneResult proves concurrent readers of one cache
// miss see one regeneration.
//
// Regeneration mints fresh MIME boundaries, so each independent pass produces
// different bytes of possibly different length. Every pass writes the cache file
// and then records the length, so two interleaved passes can leave the file holding
// one pass's bytes while the index records the other's, breaking the invariant this
// path exists to hold: that RFC822.SIZE equals the bytes served. A client fetching
// by that size then reads the wrong number of bytes.
//
// A mailbox is opened per request, so the readers below hold different *Store
// values over one directory, exactly as two concurrent requests do.
func TestConcurrentRegenerationYieldsOneResult(t *testing.T) {
	seed := openSeededStore(t)
	dir := seed.Dir()
	info, err := seed.AppendMessage(mapi.PrivateFIDInbox, multipartRaw(), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	mid := midString(uint64(info.ID))
	if err := os.Remove(seed.emlPath(mid)); err != nil {
		t.Fatal(err)
	}

	const readers = 8
	results := make([][]byte, readers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := Open(dir)
			if err != nil {
				t.Error(err)
				return
			}
			defer st.Close()
			<-start
			raw, err := st.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
			if err != nil {
				t.Error(err)
				return
			}
			results[i] = raw
		}()
	}
	close(start)
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}

	// Every reader must have been served the same message. Independent
	// regenerations differ in their boundary, so a mismatch here IS the duplicated
	// work, observed at the only place it is visible to a client.
	for i, got := range results {
		if len(got) == 0 {
			t.Fatalf("reader %d got nothing", i)
		}
		if !bytes.Equal(got, results[0]) {
			t.Errorf("reader %d was served different bytes than reader 0", i)
		}
	}

	// And the recorded size has to describe the bytes on disk, whichever pass won.
	cached, err := os.ReadFile(seed.emlPath(mid))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached, results[0]) {
		t.Error("the cached file is not the message the readers were served")
	}
	var idxSize int64
	if err := seed.idxdb.QueryRow(`SELECT size FROM messages WHERE message_id=?`, info.ID).Scan(&idxSize); err != nil {
		t.Fatal(err)
	}
	if idxSize != int64(len(cached)) {
		t.Errorf("index size %d does not match the %d cached bytes, so RFC822.SIZE lies", idxSize, len(cached))
	}
}

// TestRegenerationGivesEachCallerItsOwnSlice proves the collapsed result is not
// shared state. Suppressed callers of one flight receive one value, and the
// cache-hit path hands every caller its own buffer, so this one must too or a
// caller that touches its bytes corrupts another's.
func TestRegenerationGivesEachCallerItsOwnSlice(t *testing.T) {
	s := openSeededStore(t)
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, multipartRaw(), time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.emlPath(midString(uint64(info.ID)))); err != nil {
		t.Fatal(err)
	}
	first, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	first[0] = 'X'
	second, err := s.GetMessageRaw(mapi.PrivateFIDInbox, info.UID)
	if err != nil {
		t.Fatal(err)
	}
	if second[0] == 'X' {
		t.Error("a caller's write reached another caller's copy")
	}
}
