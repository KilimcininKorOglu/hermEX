package ews

import (
	"strings"
	"testing"
)

// availabilityReqMany builds a GetUserAvailability naming the same mailbox n times,
// which is what an amplification request looks like: every entry is permitted (the
// caller owns it) and every entry costs a store open and a calendar scan.
func availabilityReqMany(target string, n int, start, end string) string {
	var mb strings.Builder
	for range n {
		mb.WriteString(`<t:MailboxData><t:Email><t:Address>`)
		mb.WriteString(target)
		mb.WriteString(`</t:Address></t:Email><t:AttendeeType>Required</t:AttendeeType></t:MailboxData>`)
	}
	return wrapRequest(`<GetUserAvailabilityRequest xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<t:TimeZone><t:Bias>0</t:Bias></t:TimeZone>` +
		`<t:MailboxDataArray>` + mb.String() + `</t:MailboxDataArray>` +
		`<t:FreeBusyViewOptions><t:TimeWindow>` +
		`<t:StartTime>` + start + `</t:StartTime><t:EndTime>` + end + `</t:EndTime>` +
		`</t:TimeWindow><t:RequestedView>Detailed</t:RequestedView></t:FreeBusyViewOptions>` +
		`</GetUserAvailabilityRequest>`)
}

// TestAvailabilityCapsTargetsPerRequest is the amplification defect. Nothing bounded
// how many mailboxes one availability request could name, and each one opens a store
// and scans a whole calendar. The request body admits tens of thousands of entries,
// and naming the caller's own address passes the permission gate every time, so a
// single authenticated request could drive that much disk work.
func TestAvailabilityCapsTargetsPerRequest(t *testing.T) {
	ts, _ := availabilityServer(t)

	SetMaxFreeBusyTargets(2)
	t.Cleanup(func() { SetMaxFreeBusyTargets(0) })

	_, body := soapPost(t, ts, availabilityReqMany("alice@hermex.test", 9, winStart, winEnd), true)
	if got := strings.Count(body, "<FreeBusyResponse>"); got != 2 {
		t.Errorf("answered %d of 9 requested targets, want the cap of 2", got)
	}
}

// TestAvailabilityBelowCapIsUnchanged is the control: an ordinary request that sits
// under the cap must still be answered in full.
func TestAvailabilityBelowCapIsUnchanged(t *testing.T) {
	ts, _ := availabilityServer(t)

	SetMaxFreeBusyTargets(10)
	t.Cleanup(func() { SetMaxFreeBusyTargets(0) })

	_, body := soapPost(t, ts, availabilityReqMany("alice@hermex.test", 3, winStart, winEnd), true)
	if got := strings.Count(body, "<FreeBusyResponse>"); got != 3 {
		t.Errorf("answered %d of 3 requested targets, want all of them", got)
	}
}
