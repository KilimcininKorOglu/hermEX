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
	info := mustAppendMessage(t, seed, mapi.PrivateFIDInbox, multipartRaw(), time.Unix(1700000000, 0), 0)
	mid := midString(uint64(info.ID))
	mustNoErr(t, "drop the eml cache", os.Remove(seed.emlPath(mid)))

	results := concurrentReads(t, dir, info.UID, 8)

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
	mustNoErr(t, "read the cached eml", err)
	if !bytes.Equal(cached, results[0]) {
		t.Error("the cached file is not the message the readers were served")
	}
	var idxSize int64
	mustScan(t, seed.idxdb.QueryRow(`SELECT size FROM messages WHERE message_id=?`, info.ID), &idxSize)
	wantEq(t, "index size against the cached bytes (RFC822.SIZE)", idxSize, int64(len(cached)))
}

// concurrentReads fetches one message's wire form from n independently opened
// stores over the same directory, exactly as n concurrent requests do, and
// returns what each reader was served.
func concurrentReads(t *testing.T, dir string, uid uint32, readers int) [][]byte {
	t.Helper()
	results := make([][]byte, readers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range readers {
		wg.Go(func() {
			st, err := Open(dir)
			if err != nil {
				t.Error(err)
				return
			}
			defer st.Close()
			<-start
			raw, err := st.GetMessageRaw(mapi.PrivateFIDInbox, uid)
			if err != nil {
				t.Error(err)
				return
			}
			results[i] = raw
		})
	}
	close(start)
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	return results
}

// TestRegenerationGivesEachCallerItsOwnSlice proves the collapsed result is not
// shared state. Suppressed callers of one flight receive one value, and the
// cache-hit path hands every caller its own buffer, so this one must too or a
// caller that touches its bytes corrupts another's.
func TestRegenerationGivesEachCallerItsOwnSlice(t *testing.T) {
	s := openSeededStore(t)
	info := mustAppendMessage(t, s, mapi.PrivateFIDInbox, multipartRaw(), time.Unix(1700000000, 0), 0)
	mustNoErr(t, "drop the eml cache", os.Remove(s.emlPath(midString(uint64(info.ID)))))
	first := mustGetMessageRaw(t, s, mapi.PrivateFIDInbox, info.UID)
	first[0] = 'X'
	second := mustGetMessageRaw(t, s, mapi.PrivateFIDInbox, info.UID)
	if second[0] == 'X' {
		t.Error("a caller's write reached another caller's copy")
	}
}
