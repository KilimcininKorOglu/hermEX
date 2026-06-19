package activesync

import (
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
