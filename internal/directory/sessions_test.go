package directory

import "testing"

// TestSessionUpsertRefresh proves a second upsert with the same id refreshes the
// mutable fields (command, last_update, addinfo, push) while preserving the
// immutable ones (user, device, start) from the initial insert.
func TestSessionUpsertRefresh(t *testing.T) {
	d, _ := freshDirectory(t)

	const now = int64(1_000_000)
	mustNoErr(t, "insert the session", d.UpsertSession(SessionRecord{
		ID: "s1", Username: "a@x.test", DeviceID: "dev1", DeviceType: "iPhone",
		IP: "10.0.0.1", Command: "Ping", ASVersion: "14.1", StartAt: now, LastUpdate: now, Push: true,
	}))
	mustNoErr(t, "refresh the session", d.UpsertSession(SessionRecord{
		ID: "s1", Username: "a@x.test", DeviceID: "dev1", Command: "Sync",
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

// TestSessionStalenessAndPurge proves the age filter hides stale rows and that
// PurgeStaleSessions deletes exactly them.
func TestSessionStalenessAndPurge(t *testing.T) {
	d, _ := freshDirectory(t)

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
	wantEq(t, "sessions shown after the purge", len(after), 2)
}
