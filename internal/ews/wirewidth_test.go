package ews

import (
	"math"
	"strconv"
	"testing"
	"time"
)

// TestSubscriptionTimeoutIsClamped pins the bound on a pull subscription's
// Timeout. The value is the client's and it decides how long the server keeps the
// subscription alive. Unbounded, a huge minute count both overflowed the 32-bit
// field the id carries and, once multiplied out to a Duration, wrapped to a
// nonsense lifetime.
func TestSubscriptionTimeoutIsClamped(t *testing.T) {
	cases := []struct {
		name string
		min  int
		want time.Duration
	}{
		{"in range", 45, 45 * time.Minute},
		{"above the spec ceiling", 1_000_000_000, maxSubscriptionTimeoutMin * time.Minute},
		{"absent", 0, 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, sess, _ := subServer(t)
			inner := `<Subscribe xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
				`<t:PullSubscriptionRequest SubscribeToAllFolders="true">` +
				`<t:EventTypes><t:EventType>NewMailEvent</t:EventType></t:EventTypes>` +
				`<t:Timeout>` + strconv.Itoa(tc.min) + `</t:Timeout>` +
				`</t:PullSubscriptionRequest></Subscribe>`
			id := subscribe(t, srv, sess, inner)

			srv.subMu.Lock()
			sub := srv.subs[id]
			srv.subMu.Unlock()
			if sub == nil {
				t.Fatal("the subscription was not registered")
			}
			if sub.timeout != tc.want {
				t.Errorf("timeout = %v, want %v", sub.timeout, tc.want)
			}
		})
	}
}

// TestRulePriorityRejectsWiderThanInt32 pins the width of the rule priority. The
// sequence is stored in 32 bits, so a wire value beyond that used to wrap: a
// priority of 2147483648 became -2147483648 and the rule jumped to the front of
// the evaluation order instead of being refused.
func TestRulePriorityRejectsWiderThanInt32(t *testing.T) {
	base := func(prio int) ewsRule {
		acts := ruleActions{Delete: true}
		return ewsRule{DisplayName: "r", Priority: prio, IsEnabled: true, Actions: &acts}
	}
	if _, _, ok := rulePatchFromWire(base(3), 0); !ok {
		t.Fatal("an in-range priority was refused")
	}
	for _, prio := range []int{math.MaxInt32 + 1, math.MinInt32 - 1} {
		if _, _, ok := rulePatchFromWire(base(prio), 0); ok {
			t.Errorf("priority %d was accepted", prio)
		}
	}
}
