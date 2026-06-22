package dav

import "testing"

// TestBodyLimitResolution proves the iCalendar/vCard PUT caps resolve to the
// operator-set value when set and the built-in default otherwise — the values the DAV
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
