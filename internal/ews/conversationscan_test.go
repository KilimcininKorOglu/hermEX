package ews

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// seedSingles appends n messages to the Inbox, each with its own Message-Id and
// subject so no two of them group into one conversation. The store is opened once:
// a per-message open would dominate the test's runtime.
func seedSingles(t *testing.T, dir string, n int, base time.Time) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for i := range n {
		raw := fmt.Sprintf("From: Filler <filler@x.test>\r\nMessage-Id: <filler-%d@x>\r\nSubject: Filler %d\r\n\r\nbody\r\n", i, i)
		if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), base.Add(time.Duration(i)*time.Second), 0); err != nil {
			t.Fatal(err)
		}
	}
}

// emlFiles counts the cached wire forms a mailbox currently holds.
func emlFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "eml"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// dropEMLCache deletes every cached wire form, leaving each message readable only
// by re-synthesizing it from the stored object.
func dropEMLCache(t *testing.T, dir string) {
	t.Helper()
	emlDir := filepath.Join(dir, "eml")
	entries, err := os.ReadDir(emlDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(emlDir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
}

// threadCount returns the member count FindConversation reports for the seeded
// two-message thread, or 0 when no conversation holds two messages.
func threadCount(t *testing.T, body string) int {
	t.Helper()
	var p parsedFindConversation
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse FindConversation: %v\n%s", err, body)
	}
	for _, c := range p.Conversations {
		if c.Topic == "Re: Project plan" {
			return c.Count
		}
	}
	return 0
}

// TestConversationGroupingSpansAMailboxPastTwoThousandMessages proves grouping
// reaches every message in the folder. Grouping used to stop after a fixed 2000
// messages, which silently dropped a conversation's remaining members: a client
// then saw a short thread, and a conversation-wide Move, Delete or SetReadState
// touched only the part that had been scanned while still answering NoError.
func TestConversationGroupingSpansAMailboxPastTwoThousandMessages(t *testing.T) {
	ts, dir := seededEWS(t)
	seedSingles(t, dir, 2000, time.Unix(1718100000, 0))
	// The thread is seeded last, so it sits past the messages the old scan read.
	seedThread(t, dir)

	_, body := soapPost(t, ts, wrapRequest(findConversationBody("inbox")), true)
	if got := threadCount(t, body); got != 2 {
		t.Errorf("the thread reports %d messages, want 2: a member past the 2000th message was dropped", got)
	}
}

// TestConversationGroupingReadsNoWireForm proves grouping derives its ids from the
// stored threading properties. With the wire-form cache dropped, a grouping pass
// that read each message back would re-synthesize it through oxcmail.Export and
// write the cache file again, so an unchanged cache is the evidence that no message
// was read.
func TestConversationGroupingReadsNoWireForm(t *testing.T) {
	ts, dir := seededEWS(t)
	seedThread(t, dir)
	dropEMLCache(t, dir)

	_, body := soapPost(t, ts, wrapRequest(findConversationBody("inbox")), true)
	if got := threadCount(t, body); got != 2 {
		t.Errorf("the thread reports %d messages, want 2: grouping needs the wire form", got)
	}
	if n := emlFiles(t, dir); n != 0 {
		t.Errorf("%d wire forms were regenerated, want 0: grouping read the messages back", n)
	}
}
