package dav

import (
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAWriteIntoAFullMailboxIsRefusedAs507 fills the mailbox past its receive
// quota, past which nothing new may enter it. RFC 4331 section 6 has a write that
// would exceed a quota answer 507 with the DAV:quota-not-exceeded precondition:
// a PUT creating an event or a contact, and a COPY, each add an object and must
// be refused with nothing stored. A PUT over an existing object and a MOVE add
// nothing, so a full mailbox can still edit and file what it holds.
func TestAWriteIntoAFullMailboxIsRefusedAs507(t *testing.T) {
	accs := seedCalendar(t)
	ts := httptest.NewServer(NewServer(accs, accs, "hermex.test").Handler())
	t.Cleanup(ts.Close)
	doFull(t, ts, "PUT", calURL("ev.ics"), timedEventICS, nil)
	fillPastQuota(t, accs[testUser].MailboxPath)

	refused := []struct{ method, path, body string }{
		{"PUT", calURL("new.ics"), strings.ReplaceAll(timedEventICS, "UID:", "UID:fresh-")},
		{"PUT", contactURL("new.vcf"), adaVCard},
		{"COPY", calURL("ev.ics"), ""},
	}
	for _, c := range refused {
		hdr := map[string]string{"Destination": calURL("copy.ics")}
		resp, body := doFull(t, ts, c.method, c.path, c.body, hdr)
		if resp.StatusCode != http.StatusInsufficientStorage || !strings.Contains(body, "quota-not-exceeded") {
			t.Errorf("%s %s into a full mailbox: status %d body %q, want 507 quota-not-exceeded", c.method, c.path, resp.StatusCode, body)
		}
	}
	for _, p := range []string{calURL("new.ics"), contactURL("new.vcf"), calURL("copy.ics")} {
		if resp := doFullStatus(t, ts, "GET", p, "", nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("a refused write was stored at %s: status %d", p, resp.StatusCode)
		}
	}
	if resp := doFullStatus(t, ts, "PUT", calURL("ev.ics"), timedEventICS, nil); resp.StatusCode >= 300 {
		t.Errorf("a PUT over an existing event in a full mailbox: status %d, want success", resp.StatusCode)
	}
}

// fillPastQuota stores a 4 KiB message and sets a 1 KiB receive quota.
func fillPastQuota(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	big := "Subject: big\r\n\r\n" + strings.Repeat("x", 4096)
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(big), time.Unix(3, 0), 0)
	if err != nil || info.UID == 0 {
		t.Fatalf("append the filler message: uid=%d err=%v", info.UID, err)
	}
	if err := st.SetQuota(objectstore.QuotaLimits{ReceiveKB: 1}); err != nil {
		t.Fatal(err)
	}
}

// TestOversizedBodyIsRefusedAs413 sends a body one byte over the operator's cap
// to each surface that reads one. RFC 9110 15.5.14 answers a body too large to
// process with 413, so a client knows to send less; 400 would tell it the content
// is malformed. Nothing may be stored from the refused PUT.
func TestOversizedBodyIsRefusedAs413(t *testing.T) {
	const limit = 64
	accs := seedCalendar(t)
	s := NewServer(accs, accs, "hermex.test")
	s.SetMaxICal(limit)
	s.SetMaxVCard(limit)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	big := strings.Repeat("x", limit+1)

	cases := []struct{ method, path string }{
		{"PUT", calURL("big.ics")},
		{"PUT", contactURL("big.vcf")},
		{"REPORT", "/dav/calendars/" + testUser + "/calendar/"},
		{"PROPPATCH", "/dav/calendars/" + testUser + "/calendar/"},
	}
	for _, c := range cases {
		if resp := doFullStatus(t, ts, c.method, c.path, big, nil); resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s with a body over the cap: status %d, want 413", c.method, c.path, resp.StatusCode)
		}
	}
	if resp := doFullStatus(t, ts, "GET", calURL("big.ics"), "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the refused PUT was stored: status %d, want 404", resp.StatusCode)
	}
}

// TestBodyLimitResolution proves the iCalendar/vCard PUT caps resolve to the
// operator-set value when set and the built-in default otherwise, the values the DAV
// daemon's poll drives, read live by the PUT handlers, applied without a restart.
func TestBodyLimitResolution(t *testing.T) {
	s := NewServer(nil, nil, "mail.test")
	if got := s.icalLimit(); got != DefaultMaxICal {
		t.Errorf("default iCal = %d, want %d", got, DefaultMaxICal)
	}
	if got := s.vcardLimit(); got != DefaultMaxVCard {
		t.Errorf("default vCard = %d, want %d", got, DefaultMaxVCard)
	}

	s.SetMaxICal(123456)
	s.SetMaxVCard(654321)
	if s.icalLimit() != 123456 || s.vcardLimit() != 654321 {
		t.Errorf("after set = ical %d / vcard %d, want 123456 / 654321", s.icalLimit(), s.vcardLimit())
	}

	s.SetMaxICal(0) // 0 restores the default
	s.SetMaxVCard(0)
	if s.icalLimit() != DefaultMaxICal || s.vcardLimit() != DefaultMaxVCard {
		t.Errorf("after reset = ical %d / vcard %d, want the defaults", s.icalLimit(), s.vcardLimit())
	}
}
