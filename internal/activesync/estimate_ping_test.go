package activesync

import (
	"strconv"
	"testing"

	"hermex/internal/wbxml"
)

// estimateReq builds a GetItemEstimate request for the Inbox at the given key.
func estimateReq(key string) *wbxml.Node {
	return wbxml.Elem(wbxml.GIEGetItemEstimate,
		wbxml.Elem(wbxml.GIECollections,
			wbxml.Elem(wbxml.GIECollection,
				wbxml.Str(wbxml.ASSyncKey, key),
				wbxml.Str(wbxml.ASCollectionID, inboxID()))))
}

// pingReq builds a Ping request watching the Inbox with the given heartbeat.
func pingReq(heartbeat string) *wbxml.Node {
	return wbxml.Elem(wbxml.PGPing,
		wbxml.Str(wbxml.PGHeartbeatInt, heartbeat),
		wbxml.Elem(wbxml.PGFolders,
			wbxml.Elem(wbxml.PGFolder,
				wbxml.Str(wbxml.PGID, inboxID()),
				wbxml.Str(wbxml.PGClass, "Email"))))
}

// TestGetItemEstimate confirms the estimate equals the count of unsynced changes.
func TestGetItemEstimate(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 3)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", "")) // snapshot now holds 3, key 2
	seedInbox(t, dir, 2)                         // two new messages since the sync

	_, root := postCommand(t, ts, "GetItemEstimate", estimateReq("2"))
	resp := root.Child(wbxml.GIEResponse)
	if resp.ChildText(wbxml.GIEStatus) != "1" {
		t.Fatalf("status = %q, want 1", resp.ChildText(wbxml.GIEStatus))
	}
	if est := resp.Child(wbxml.GIECollection).ChildText(wbxml.GIEEstimate); est != "2" {
		t.Errorf("estimate = %q, want 2", est)
	}
}

// TestGetItemEstimateNotPrimed confirms an unsynced collection reports Status 2.
func TestGetItemEstimateNotPrimed(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)

	_, root := postCommand(t, ts, "GetItemEstimate", estimateReq("5"))
	resp := root.Child(wbxml.GIEResponse)
	if resp.ChildText(wbxml.GIEStatus) != "2" {
		t.Errorf("status = %q, want 2 (not primed)", resp.ChildText(wbxml.GIEStatus))
	}
}

// TestPingHeartbeatOutOfRange confirms an out-of-range heartbeat reports Status 5
// with the nearest acceptable interval, rather than silently holding for a
// different interval than the device asked for.
func TestPingHeartbeatOutOfRange(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "Ping", pingReq("99999"))
	if s := root.ChildText(wbxml.PGStatus); s != "5" {
		t.Errorf("status = %q, want 5 (heartbeat out of range)", s)
	}
	if hb := root.ChildText(wbxml.PGHeartbeatInt); hb != "3540" {
		t.Errorf("HeartbeatInterval = %q, want 3540 (the upper bound)", hb)
	}
}

// TestPingRefusesAMalformedHeartbeat proves the MS-ASCMD Status (Ping) table: a
// heartbeat that is too short or too long is answered Status 5 with the shortest
// or longest allowed interval, and one that is not a number is a syntax error,
// Status 4. The watched folder is unsynced, so an accepted heartbeat answers
// Status 7 at once instead of holding the request.
func TestPingRefusesAMalformedHeartbeat(t *testing.T) {
	ts, _ := seededServer(t)
	status := func(hb string) (string, string) {
		_, root := postCommand(t, ts, "Ping", pingReq(hb))
		return root.ChildText(wbxml.PGStatus), root.ChildText(wbxml.PGHeartbeatInt)
	}
	s, shortest := status("0")
	wantEq(t, "the status for a zero heartbeat", s, "5")
	for _, hb := range []string{"-5", "-99999999999999999999"} {
		s, got := status(hb)
		wantEq(t, "the status for heartbeat "+hb, s, "5")
		wantEq(t, "the bound for heartbeat "+hb, got, shortest)
	}
	s, longest := status("99999")
	wantEq(t, "the status for a long heartbeat", s, "5")
	// 18446744075 seconds wraps a nanosecond Duration to about 1.3s.
	for _, hb := range []string{"18446744075", "99999999999999999999"} {
		s, got := status(hb)
		wantEq(t, "the status for heartbeat "+hb, s, "5")
		wantEq(t, "the bound for heartbeat "+hb, got, longest)
	}
	lo, _ := strconv.Atoi(shortest)
	hi, _ := strconv.Atoi(longest)
	if lo <= 0 || lo >= hi {
		t.Errorf("shortest %q and longest %q are not an allowed range", shortest, longest)
	}
	for _, hb := range []string{shortest, longest} {
		s, _ := status(hb)
		wantEq(t, "the status for the returned bound "+hb, s, "7")
	}
	for _, hb := range []string{"abc", "1.5", " 30"} {
		s, _ := status(hb)
		wantEq(t, "the status for heartbeat "+strconv.Quote(hb), s, "4")
	}
}

// TestPingNoFolders confirms a Ping naming no folder to watch reports Status 3.
func TestPingNoFolders(t *testing.T) {
	ts, _ := seededServer(t)

	_, root := postCommand(t, ts, "Ping", wbxml.Elem(wbxml.PGPing, wbxml.Str(wbxml.PGHeartbeatInt, "30")))
	if s := root.ChildText(wbxml.PGStatus); s != "3" {
		t.Errorf("status = %q, want 3 (no pingable folders)", s)
	}
}

// TestPingFolderNotSynced confirms watching a folder the device has not synced
// reports Status 7 (the device must sync it first) rather than silently ignoring
// it and holding the heartbeat.
func TestPingFolderNotSynced(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)

	_, root := postCommand(t, ts, "Ping", pingReq("30"))
	if s := root.ChildText(wbxml.PGStatus); s != "7" {
		t.Errorf("status = %q, want 7 (folder needs sync)", s)
	}
}

// TestPingNoChange confirms the heartbeat expires with Status 1 when nothing
// changes.
func TestPingNoChange(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", "")) // snapshot synced

	_, root := postCommand(t, ts, "Ping", pingReq("1"))
	if root.ChildText(wbxml.PGStatus) != "1" {
		t.Errorf("status = %q, want 1 (heartbeat expired)", root.ChildText(wbxml.PGStatus))
	}
}

// TestPingDetectsChange confirms a new message makes Ping return Status 2 with
// the changed folder, without waiting out the heartbeat.
func TestPingDetectsChange(t *testing.T) {
	ts, dir := seededServer(t)
	seedInbox(t, dir, 1)
	postCommand(t, ts, "Sync", syncReq("0", ""))
	postCommand(t, ts, "Sync", syncReq("1", ""))
	seedInbox(t, dir, 1) // a new message arrives after the sync

	_, root := postCommand(t, ts, "Ping", pingReq("30"))
	if root.ChildText(wbxml.PGStatus) != "2" {
		t.Fatalf("status = %q, want 2 (changes)", root.ChildText(wbxml.PGStatus))
	}
	folders := root.Child(wbxml.PGFolders)
	if folders == nil || folders.ChildText(wbxml.PGFolder) != inboxID() {
		t.Errorf("Ping did not report the Inbox as changed")
	}
}
