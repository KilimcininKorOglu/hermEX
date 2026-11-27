package directory

import (
	"strings"
	"testing"
)

// TestSessionUpsertRefresh proves a second upsert with the same id refreshes the
// mutable fields (command, last_update, addinfo, push) while preserving the
// immutable ones (user, device, start) from the initial insert.
func TestSessionUpsertRefresh(t *testing.T) {
	d := freshSQL(t)

	const now = int64(1_000_000)
	mustNoErr(t, "insert the session", d.UpsertSession(SessionRecord{
		ID: "s1", Username: "a@x.test", DeviceID: "dev1", DeviceType: "iPhone",
		IP: "10.0.0.1", Command: "Ping", ASVersion: "14.1", StartAt: now, LastUpdate: now, Push: true,
	}))
	mustNoErr(t, "refresh the session", d.UpsertSession(SessionRecord{
		ID: "s1", Username: "other@x.test", DeviceID: "dev2", DeviceType: "Android", IP: "10.0.0.9", Command: "Sync",
		StartAt: now, LastUpdate: now + 5, Push: false, AddInfo: "working",
	}))

	list, err := d.ListActiveSessions(now + 10)
	mustNoErr(t, "list active sessions", err)
	if len(list) != 1 {
		t.Fatalf("got %d sessions, want 1 (upsert must not duplicate)", len(list))
	}
	s := list[0]
	wantEq(t, "refreshed command", s.Command, "Sync")
	wantEq(t, "refreshed last update", s.LastUpdate, now+5)
	wantEq(t, "refreshed addinfo", s.AddInfo, "working")
	wantEq(t, "refreshed push flag", s.Push, false)
	wantEq(t, "preserved username", s.Username, "a@x.test")
	wantEq(t, "preserved device id", s.DeviceID, "dev1")
	wantEq(t, "preserved device type", s.DeviceType, "iPhone")
	wantEq(t, "preserved ip", s.IP, "10.0.0.1")
	wantEq(t, "preserved start time", s.StartAt, now)
}

// TestSessionsKeepAnOverlongClientValue gives both session stores device and agent
// strings longer than their columns, in multi-byte characters as a hostile client
// may: each session must still be written (an EAS one shows in the monitor, a
// webmail one stays signed in) with the value cut to the column width.
func TestSessionsKeepAnOverlongClientValue(t *testing.T) {
	d := freshSQL(t)
	long := strings.Repeat("ş", 1000)
	const now = int64(1_000_000)
	mustNoErr(t, "write the EAS session", d.UpsertSession(SessionRecord{
		ID: "s1", Username: "a@x.test", DeviceID: long, DeviceType: long, UserAgent: long,
		Command: long, ASVersion: long, AddInfo: long, StartAt: now, LastUpdate: now,
	}))
	list, err := d.ListActiveSessions(now + 1)
	mustNoErr(t, "list active sessions", err)
	if len(list) != 1 {
		t.Fatalf("got %d sessions, want the one written", len(list))
	}
	wantEq(t, "stored user agent", list[0].UserAgent, strings.Repeat("ş", 255))
	wantEq(t, "stored device type", list[0].DeviceType, strings.Repeat("ş", 64))

	mustNoErr(t, "write the webmail session", d.CreateWebmailSession(WebmailSession{
		Jti: "j1", Email: "a@x.test", DeviceType: long, UserAgent: long, ClientIP: long,
		CreatedAt: now, LastActive: now, ExpiresAt: now + 3600,
	}))
	active, err := d.WebmailSessionActive("j1", now+1)
	mustNoErr(t, "check the webmail session", err)
	wantEq(t, "webmail session active", active, true)
}

// TestSessionStalenessAndPurge proves the age filter hides stale rows and that
// PurgeStaleSessions deletes exactly them.
func TestSessionStalenessAndPurge(t *testing.T) {
	d := freshSQL(t)

	const now = int64(1_000_000)
	seed := []SessionRecord{
		{ID: "fresh", Username: "a@x.test", LastUpdate: now, EndedAt: 0},           // running, fresh -> shown
		{ID: "stale-run", Username: "b@x.test", LastUpdate: now - 200, EndedAt: 0}, // running, >120s -> hidden
		{ID: "ended-recent", LastUpdate: now - 100, EndedAt: now - 5},              // ended <20s ago -> shown
		{ID: "ended-old", LastUpdate: now - 100, EndedAt: now - 30},                // ended >20s ago -> hidden
	}
	for _, s := range seed {
		mustNoErr(t, "seed session "+s.ID, d.UpsertSession(s))
	}

	shown := map[string]bool{}
	list, err := d.ListActiveSessions(now)
	mustNoErr(t, "list active sessions", err)
	for _, s := range list {
		shown[s.ID] = true
	}
	wantEq(t, "the fresh running session is shown", shown["fresh"], true)
	wantEq(t, "the recently ended session is shown", shown["ended-recent"], true)
	wantEq(t, "the stale running session is shown", shown["stale-run"], false)
	wantEq(t, "the long-ended session is shown", shown["ended-old"], false)

	n, err := d.PurgeStaleSessions(now)
	mustNoErr(t, "purge stale sessions", err)
	wantEq(t, "rows purged (stale-run and ended-old)", n, int64(2))
	// The fresh rows survive the purge.
	after, err := d.ListActiveSessions(now)
	mustNoErr(t, "list active sessions", err)
	kept := map[string]bool{}
	for _, s := range after {
		kept[s.ID] = true
	}
	wantEq(t, "sessions shown after the purge", len(after), 2)
	wantEq(t, "the fresh session after the purge", kept["fresh"], true)
	wantEq(t, "the recently ended session after the purge", kept["ended-recent"], true)
}
