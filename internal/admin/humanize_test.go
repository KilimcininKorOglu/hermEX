package admin

import (
	"testing"
	"time"
)

// TestClockStamp proves a time renders in the operator's zone, reads as a
// distance from now in both directions under a day, and from a day on reads as
// its date and time in the operator's language.
func TestClockStamp(t *testing.T) {
	ist, err := time.LoadLocation("Europe/Istanbul")
	mustNoErr(t, err, "load a zone")
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	c := clock{loc: ist, now: now, lang: "tr"}

	s := c.at(now.Add(-90 * time.Minute))
	wantEq(t, s.Short, "28.09.2026 13:30", "the Turkish date and time in the operator's zone")
	wantEq(t, s.Full, "28.09.2026 13:30 Europe/Istanbul", "the tooltip names the zone")
	en := clock{loc: ist, now: now, lang: "en"}.at(now.Add(-90 * time.Minute))
	wantEq(t, en.Short, "09/28/2026 1:30 PM", "the English date and time")
	wantEq(t, s.ISO, "2026-09-28T10:30:00Z", "the machine-readable instant")

	cases := []struct {
		d      time.Duration
		en, tr string
	}{
		{-300 * time.Millisecond, "just now", "az önce"},
		{-20 * time.Second, "20 s ago", "20 sn önce"},
		{45 * time.Second, "in 45 s", "45 sn sonra"},
		{-5 * time.Minute, "5 min ago", "5 dk önce"},
		{-90 * time.Minute, "1 h ago", "1 sa önce"},
		{-23 * time.Hour, "23 h ago", "23 sa önce"},
		{500 * time.Millisecond, "shortly", "birazdan"},
		{4 * time.Minute, "in 4 min", "4 dk sonra"},
		{23*time.Hour + 59*time.Minute, "in 23 h", "23 sa sonra"},
	}
	for _, tc := range cases {
		rel := c.at(now.Add(tc.d)).Rel
		wantEq(t, translate("en", rel), tc.en, "English distance")
		wantEq(t, translate("tr", rel), tc.tr, "Turkish distance")
	}
	wantEq(t, c.at(now.Add(-24*time.Hour)).Rel, "", "a day ago reads as its date")
	wantEq(t, c.at(now.Add(3*24*time.Hour)).Rel, "", "three days ahead reads as its date")
	wantEq(t, c.unix(0), stamp{}, "an unset time renders nothing")

	utc := clock{loc: time.UTC, now: now, lang: "tr"}.at(now)
	wantEq(t, utc.Full, "28.09.2026 12:00 UTC", "an operator without a zone sees UTC, labelled")
}

// TestDurationMsg proves a length of time reads in its two largest units in
// both languages rather than as a raw second count.
func TestDurationMsg(t *testing.T) {
	cases := []struct {
		secs   int64
		en, tr string
	}{
		{42, "42s", "42 sn"},
		{125, "2m 5s", "2 dk 5 sn"},
		{7260, "2h 1m", "2 sa 1 dk"},
		{190000, "2d 4h", "2 gün 4 sa"},
		{-5, "0s", "0 sn"},
	}
	for _, c := range cases {
		m := durationMsg(c.secs)
		wantEq(t, translate("en", m), c.en, "English duration")
		wantEq(t, translate("tr", m), c.tr, "Turkish duration")
	}
}

// TestSizeMsg proves a byte count reads in the largest fitting unit, with one
// decimal under 10 in the language's decimal separator.
func TestSizeMsg(t *testing.T) {
	cases := []struct {
		bytes  int64
		en, tr string
	}{
		{512, "512 B", "512 bayt"},
		{48213, "47 KB", "47 KB"},
		{1536 * 1024, "1.5 MB", "1,5 MB"},
		{2 << 30, "2 GB", "2 GB"},
		{20480 * 1024, "20 MB", "20 MB"},
	}
	for _, c := range cases {
		wantEq(t, translate("en", sizeMsg(c.bytes, "en")), c.en, "English size")
		wantEq(t, translate("tr", sizeMsg(c.bytes, "tr")), c.tr, "Turkish size")
	}
}
