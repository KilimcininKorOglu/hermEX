package webmail

import (
	"testing"
	"time"

	"hermex/internal/objectstore"
)

func mi(id int64, uid uint32, date int64) objectstore.MessageInfo {
	return objectstore.MessageInfo{ID: id, UID: uid, InternalDate: time.Unix(date, 0)}
}

// threadIDs returns the message ids of a thread in order, for assertions.
func threadIDs(t thread) []int64 {
	ids := make([]int64, len(t.Messages))
	for i, m := range t.Messages {
		ids[i] = m.ID
	}
	return ids
}

// TestBuildThreadsMissingRoot is the discriminating case: two messages whose
// common ancestor (<root@x>) is NOT in the folder must still be grouped, via
// their shared reference. Naive parent-lookup grouping gets this wrong.
func TestBuildThreadsMissingRoot(t *testing.T) {
	msgs := []objectstore.MessageInfo{mi(1, 1, 100), mi(2, 2, 200)}
	headers := map[int64]objectstore.ThreadHeaders{
		1: {MessageID: "<a@x>", References: "<root@x>"},
		2: {MessageID: "<b@x>", References: "<root@x>"},
	}
	threads := buildThreads(msgs, headers)
	if len(threads) != 1 {
		t.Fatalf("want 1 thread (shared missing ancestor), got %d", len(threads))
	}
	if got := threadIDs(threads[0]); len(got) != 2 {
		t.Errorf("thread has %v, want both messages", got)
	}
}

// TestBuildThreadsLatestOrdering checks threads order by newest activity (an old
// root with a recent reply beats a newer-but-quiet thread) and that members are
// oldest-first within a thread.
func TestBuildThreadsLatestOrdering(t *testing.T) {
	msgs := []objectstore.MessageInfo{
		mi(1, 1, 100), // root of thread A
		mi(2, 2, 300), // recent reply in thread A
		mi(3, 3, 200), // lone thread B, newer than A's root but older than A's reply
	}
	headers := map[int64]objectstore.ThreadHeaders{
		1: {MessageID: "<a@x>"},
		2: {MessageID: "<b@x>", References: "<a@x>", InReplyTo: "<a@x>"},
		3: {MessageID: "<c@x>"},
	}
	threads := buildThreads(msgs, headers)
	if len(threads) != 2 {
		t.Fatalf("want 2 threads, got %d", len(threads))
	}
	// Thread A first (latest activity 300 > 200).
	if got := threadIDs(threads[0]); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("first thread = %v, want [1 2] (A, oldest-first)", got)
	}
	if got := threadIDs(threads[1]); len(got) != 1 || got[0] != 3 {
		t.Errorf("second thread = %v, want [3] (B)", got)
	}
}

// TestBuildThreadsSingletons checks messages with no threading headers each form
// their own thread, newest-first.
func TestBuildThreadsSingletons(t *testing.T) {
	msgs := []objectstore.MessageInfo{mi(1, 1, 100), mi(2, 2, 200)}
	threads := buildThreads(msgs, map[int64]objectstore.ThreadHeaders{})
	if len(threads) != 2 {
		t.Fatalf("want 2 singleton threads, got %d", len(threads))
	}
	if threadIDs(threads[0])[0] != 2 || threadIDs(threads[1])[0] != 1 {
		t.Errorf("singletons not ordered newest-first: %v, %v", threadIDs(threads[0]), threadIDs(threads[1]))
	}
}

// TestBuildThreadsReplyChain checks a straightforward root+reply links into one
// thread, ordered oldest-first.
func TestBuildThreadsReplyChain(t *testing.T) {
	msgs := []objectstore.MessageInfo{mi(2, 2, 300), mi(1, 1, 100)} // out of order on input
	headers := map[int64]objectstore.ThreadHeaders{
		1: {MessageID: "<a@x>"},
		2: {MessageID: "<b@x>", References: "<a@x>"},
	}
	threads := buildThreads(msgs, headers)
	if len(threads) != 1 {
		t.Fatalf("want 1 thread, got %d", len(threads))
	}
	if got := threadIDs(threads[0]); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("thread = %v, want [1 2] (root then reply)", got)
	}
}
