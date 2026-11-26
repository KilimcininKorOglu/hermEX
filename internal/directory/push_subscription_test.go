package directory

import "testing"

// TestPushSubscriptionRoundTrip proves a web-push subscription is stored under the
// lowercased email, re-saving the same endpoint upserts rather than duplicates, the
// poll loop can enumerate distinct subscribers, and a subscription is removable.
func TestPushSubscriptionRoundTrip(t *testing.T) {
	d, _ := freshDirectory(t)

	sub := PushSubscription{Endpoint: "https://push.example/abc", Email: "Alice@hermex.test", P256dh: "key1", Auth: "auth1", CreatedAt: 100}
	mustNoErr(t, "save subscription", d.SavePushSubscription(sub))
	got := mustOneSubscription(t, d, "stored under the lowercased email")
	wantEq(t, "stored endpoint", got.Endpoint, sub.Endpoint)
	wantEq(t, "stored key", got.P256dh, "key1")

	// Re-saving the same endpoint upserts (new keys), not duplicates.
	sub.P256dh = "key2"
	mustNoErr(t, "re-save the subscription", d.SavePushSubscription(sub))
	wantEq(t, "key after the upsert", mustOneSubscription(t, d, "after the upsert").P256dh, "key2")

	// A second device, then distinct subscriber enumeration for the poll loop.
	mustNoErr(t, "save a second device", d.SavePushSubscription(PushSubscription{
		Endpoint: "https://push.example/xyz", Email: "alice@hermex.test", P256dh: "k", Auth: "a", CreatedAt: 101,
	}))
	emails, err := d.PushSubscriberEmails()
	mustNoErr(t, "enumerate subscribers", err)
	if len(emails) != 1 {
		t.Fatalf("subscriber emails = %v, want [alice@hermex.test]", emails)
	}
	wantEq(t, "the distinct subscriber", emails[0], "alice@hermex.test")

	// Delete by endpoint leaves the other device.
	mustNoErr(t, "delete a subscription", d.DeletePushSubscription("https://push.example/abc"))
	wantEq(t, "the surviving endpoint",
		mustOneSubscription(t, d, "after the delete").Endpoint, "https://push.example/xyz")
}

// TestPushSubscriptionKeepsItsOwner proves an endpoint already saved for one
// account is not moved to another by a later save naming it: the endpoint is the
// device's address, so taking it over would send one user's mail notices to the
// other's browser and silence the owner's.
func TestPushSubscriptionKeepsItsOwner(t *testing.T) {
	d, _ := freshDirectory(t)
	mustNoErr(t, "save alice's subscription", d.SavePushSubscription(PushSubscription{
		Endpoint: "https://push.example/abc", Email: "alice@hermex.test", P256dh: "key1", Auth: "auth1", CreatedAt: 100,
	}))
	err := d.SavePushSubscription(PushSubscription{
		Endpoint: "https://push.example/abc", Email: "mallory@hermex.test", P256dh: "evil", Auth: "evil", CreatedAt: 200,
	})
	wantErr(t, "another account took over alice's endpoint", err)
	got := mustOneSubscription(t, d, "after the takeover attempt")
	wantEq(t, "the endpoint's owner", got.Email, "alice@hermex.test")
	wantEq(t, "the endpoint's key", got.P256dh, "key1")
	mallory, err := d.ListPushSubscriptions("mallory@hermex.test")
	mustNoErr(t, "list mallory's subscriptions", err)
	wantEq(t, "mallory's subscriptions", len(mallory), 0)
}

// mustOneSubscription reads alice's subscriptions, requiring exactly one.
func mustOneSubscription(t *testing.T, d *SQLDirectory, what string) PushSubscription {
	t.Helper()
	got, err := d.ListPushSubscriptions("alice@hermex.test")
	mustNoErr(t, "list subscriptions", err)
	if len(got) != 1 {
		t.Fatalf("subscriptions %s = %+v, want one", what, got)
	}
	return got[0]
}
