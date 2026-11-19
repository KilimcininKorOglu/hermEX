package dav

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
		if resp, _ := doFull(t, ts, c.method, c.path, big, nil); resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s with a body over the cap: status %d, want 413", c.method, c.path, resp.StatusCode)
		}
	}
	if resp, _ := doFull(t, ts, "GET", calURL("big.ics"), "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the refused PUT was stored: status %d, want 404", resp.StatusCode)
	}
}

// TestBodyLimitResolution proves the iCalendar/vCard PUT caps resolve to the
// operator-set value when set and the built-in default otherwise, the values the DAV
// daemon's poll drives, read live by the PUT handlers, applied without a restart.
func TestBodyLimitResolution(t *testing.T) {
	s := NewServer(nil, nil, "mail.test")
	if got := s.icalLimit(); got != defaultMaxICal {
		t.Errorf("default iCal = %d, want %d", got, defaultMaxICal)
	}
	if got := s.vcardLimit(); got != defaultMaxVCard {
		t.Errorf("default vCard = %d, want %d", got, defaultMaxVCard)
	}

	s.SetMaxICal(123456)
	s.SetMaxVCard(654321)
	if s.icalLimit() != 123456 || s.vcardLimit() != 654321 {
		t.Errorf("after set = ical %d / vcard %d, want 123456 / 654321", s.icalLimit(), s.vcardLimit())
	}

	s.SetMaxICal(0) // 0 restores the default
	s.SetMaxVCard(0)
	if s.icalLimit() != defaultMaxICal || s.vcardLimit() != defaultMaxVCard {
		t.Errorf("after reset = ical %d / vcard %d, want the defaults", s.icalLimit(), s.vcardLimit())
	}
}
