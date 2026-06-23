package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
)

// TestApplyDAVSizeLimits proves both PUT body caps are applied only on a clean read;
// a read error or a missing row leaves both untouched. Without the guard a transient
// directory failure would zero the iCal and vCard caps on a running server.
func TestApplyDAVSizeLimits(t *testing.T) {
	const sentinel int64 = -1

	ical, vcard := sentinel, sentinel
	applyDAVSizeLimits(
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{DAVICalBytes: 1000, DAVVCardBytes: 2000}, true, nil
		},
		func(n int64) { ical = n }, func(n int64) { vcard = n })
	if ical != 1000 || vcard != 2000 {
		t.Errorf("applied caps = ical %d, vcard %d; want 1000, 2000", ical, vcard)
	}

	ical, vcard = sentinel, sentinel
	applyDAVSizeLimits(
		func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, errors.New("db down") },
		func(n int64) { ical = n }, func(n int64) { vcard = n })
	if ical != sentinel || vcard != sentinel {
		t.Errorf("setters called on read error (ical %d, vcard %d); the caps must be left unchanged", ical, vcard)
	}

	ical, vcard = sentinel, sentinel
	applyDAVSizeLimits(
		func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil },
		func(n int64) { ical = n }, func(n int64) { vcard = n })
	if ical != sentinel || vcard != sentinel {
		t.Errorf("setters called with no stored row (ical %d, vcard %d); the defaults must stand", ical, vcard)
	}
}
