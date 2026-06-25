package directory

import "testing"

// TestPushSubscriptionRoundTrip proves a web-push subscription is stored under the
// lowercased email, re-saving the same endpoint upserts rather than duplicates, the
// poll loop can enumerate distinct subscribers, and a subscription is removable.
func TestPushSubscriptionRoundTrip(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	if err := d.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	cleanTables(t, db)

	sub := PushSubscription{Endpoint: "https://push.example/abc", Email: "Alice@hermex.test", P256dh: "key1", Auth: "auth1", CreatedAt: 100}
	if err := d.SavePushSubscription(sub); err != nil {
		t.Fatal(err)
	}
	got, err := d.ListPushSubscriptions("alice@hermex.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Endpoint != sub.Endpoint || got[0].P256dh != "key1" {
		t.Fatalf("list = %+v, want one subscription with key1 under the lowercased email", got)
	}

	// Re-saving the same endpoint upserts (new keys), not duplicates.
	sub.P256dh = "key2"
	if err := d.SavePushSubscription(sub); err != nil {
		t.Fatal(err)
	}
	got, _ = d.ListPushSubscriptions("alice@hermex.test")
	if len(got) != 1 || got[0].P256dh != "key2" {
		t.Fatalf("after upsert = %+v, want one subscription with key2", got)
	}

	// A second device, then distinct subscriber enumeration for the poll loop.
	if err := d.SavePushSubscription(PushSubscription{Endpoint: "https://push.example/xyz", Email: "alice@hermex.test", P256dh: "k", Auth: "a", CreatedAt: 101}); err != nil {
		t.Fatal(err)
	}
	emails, err := d.PushSubscriberEmails()
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 1 || emails[0] != "alice@hermex.test" {
		t.Fatalf("subscriber emails = %v, want [alice@hermex.test]", emails)
	}

	// Delete by endpoint leaves the other device.
	if err := d.DeletePushSubscription("https://push.example/abc"); err != nil {
		t.Fatal(err)
	}
	got, _ = d.ListPushSubscriptions("alice@hermex.test")
	if len(got) != 1 || got[0].Endpoint != "https://push.example/xyz" {
		t.Fatalf("after delete = %+v, want only the xyz subscription", got)
	}
}
