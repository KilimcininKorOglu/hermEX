package directory

import "testing"

// TestSessionUpsertRefresh proves a second upsert with the same id refreshes the
// mutable fields (command, last_update, addinfo, push) while preserving the
// immutable ones (user, device, start) from the initial insert.
func TestSessionUpsertRefresh(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	const now = int64(1_000_000)
	if err := d.UpsertSession(SessionRecord{
		ID: "s1", Username: "a@x.test", DeviceID: "dev1", DeviceType: "iPhone",
		IP: "10.0.0.1", Command: "Ping", ASVersion: "14.1", StartAt: now, LastUpdate: now, Push: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertSession(SessionRecord{
		ID: "s1", Username: "a@x.test", DeviceID: "dev1", Command: "Sync",
		StartAt: now, LastUpdate: now + 5, Push: false, AddInfo: "working",
	}); err != nil {
		t.Fatal(err)
	}

	list, err := d.ListActiveSessions(now + 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d sessions, want 1 (upsert must not duplicate)", len(list))
	}
	s := list[0]
	if s.Command != "Sync" || s.LastUpdate != now+5 || s.AddInfo != "working" || s.Push {
		t.Errorf("refreshed fields = %+v, want command Sync / lastUpdate %d / addinfo working / push false", s, now+5)
	}
	if s.Username != "a@x.test" || s.DeviceID != "dev1" || s.DeviceType != "iPhone" || s.IP != "10.0.0.1" || s.StartAt != now {
		t.Errorf("immutable fields not preserved from insert: %+v", s)
	}
}

// TestSessionStalenessAndPurge proves the age filter hides stale rows and that
// PurgeStaleSessions deletes exactly them.
func TestSessionStalenessAndPurge(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	const now = int64(1_000_000)
	seed := []SessionRecord{
		{ID: "fresh", Username: "a@x.test", LastUpdate: now, EndedAt: 0},           // running, fresh -> shown
		{ID: "stale-run", Username: "b@x.test", LastUpdate: now - 200, EndedAt: 0}, // running, >120s -> hidden
		{ID: "ended-recent", LastUpdate: now - 100, EndedAt: now - 5},              // ended <20s ago -> shown
		{ID: "ended-old", LastUpdate: now - 100, EndedAt: now - 30},                // ended >20s ago -> hidden
	}
	for _, s := range seed {
		if err := d.UpsertSession(s); err != nil {
			t.Fatal(err)
		}
	}

	shown := map[string]bool{}
	list, err := d.ListActiveSessions(now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		shown[s.ID] = true
	}
	if !shown["fresh"] || !shown["ended-recent"] || shown["stale-run"] || shown["ended-old"] {
		t.Errorf("active set = %v, want {fresh, ended-recent} only", shown)
	}

	n, err := d.PurgeStaleSessions(now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("purged %d rows, want 2 (stale-run, ended-old)", n)
	}
	// The fresh rows survive the purge.
	after, _ := d.ListActiveSessions(now)
	if len(after) != 2 {
		t.Errorf("after purge %d shown, want 2", len(after))
	}
}
