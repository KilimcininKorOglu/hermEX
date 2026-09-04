package objectstore

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hermex/internal/mapi"
)

// TestRefreshEMLCollapsesConcurrentRegenerations proves an edit-driven refresh
// joins the same flight a read miss uses. Each regeneration mints fresh MIME
// boundaries, so two uncollapsed passes over one message rename two different
// files and record two different sizes, and the file that survives can come from
// a different pass than the size that survives. A client that reads RFC822.SIZE
// and then fetches that many bytes gets a truncated or over-long message.
//
// The first refresh is held inside the regeneration; a second one is then started.
// Collapsed, it waits and never regenerates, so the body runs exactly once.
func TestRefreshEMLCollapsesConcurrentRegenerations(t *testing.T) {
	s := openSeededStore(t)
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: cached\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbody bytes\r\n")
	info, err := s.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}

	var entered atomic.Int32
	inside := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.regenHook = func() {
		entered.Add(1)
		once.Do(func() { close(inside) })
		<-release
	}

	var wg sync.WaitGroup
	wg.Go(func() { s.refreshEML(info.ID) })
	<-inside // the first refresh is now inside the regeneration, holding the flight

	wg.Go(func() { s.refreshEML(info.ID) })
	// Give the second refresh time to reach the regeneration if nothing collapses
	// it. This only makes the failing direction reliable: a collapsed refresh never
	// enters the body no matter how long it waits.
	time.Sleep(200 * time.Millisecond)

	close(release)
	wg.Wait()

	if n := entered.Load(); n != 1 {
		t.Errorf("the message was regenerated %d times concurrently, want 1", n)
	}
}
