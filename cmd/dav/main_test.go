package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// davLimits captures what applyDAVSizeLimits hands to its three setters.
type davLimits struct {
	ical, vcard, freeBusyTargets int64
}

// applyWith runs applyDAVSizeLimits over one read result and returns what each
// setter received; a setter that was not called keeps -1.
func applyWith(read func() (directory.SizeLimits, bool, error)) davLimits {
	got := davLimits{ical: -1, vcard: -1, freeBusyTargets: -1}
	applyDAVSizeLimits(nil, read,
		func(n int64) { got.ical = n },
		func(n int64) { got.vcard = n },
		func(n int64) { got.freeBusyTargets = n })
	return got
}

// TestApplyDAVSizeLimits proves the three operator-set limits the size-limits row
// carries for DAV (the iCal and vCard PUT body caps and the free/busy target cap)
// all reach the running daemon on a clean read, and that a read error or a missing
// row leaves every one untouched. Without the guard a transient directory failure
// would zero the caps on a running server.
func TestApplyDAVSizeLimits(t *testing.T) {
	stored := directory.SizeLimits{DAVICalBytes: 1000, DAVVCardBytes: 2000, FreeBusyMaxTargets: 7}
	got := applyWith(func() (directory.SizeLimits, bool, error) { return stored, true, nil })
	if got != (davLimits{ical: 1000, vcard: 2000, freeBusyTargets: 7}) {
		t.Errorf("applied = %+v, want the stored ical 1000, vcard 2000 and free/busy 7", got)
	}

	untouched := davLimits{ical: -1, vcard: -1, freeBusyTargets: -1}
	// found is true beside the error, so only the error guard stands between a
	// half-read row and the setters: the !found guard cannot hide its loss.
	got = applyWith(func() (directory.SizeLimits, bool, error) { return stored, true, errors.New("db down") })
	if got != untouched {
		t.Errorf("a read error applied %+v; every cap must be left unchanged", got)
	}

	got = applyWith(func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil })
	if got != untouched {
		t.Errorf("a missing row applied %+v; the defaults must stand", got)
	}
}

// captureSink collects the events an applier emits.
type captureSink struct{ events []logging.Event }

func (s *captureSink) Write(e logging.Event) { s.events = append(s.events, e) }

// TestApplyDAVSizeLimitsRecordsAReadFailure proves the read failure the applier
// swallows to keep the running caps is still recorded in the central log, the
// operator's only view of a settings row that stopped being read.
func TestApplyDAVSizeLimitsRecordsAReadFailure(t *testing.T) {
	sink := &captureSink{}
	applyDAVSizeLimits(logging.New(sink),
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{}, false, errors.New("db down")
		},
		func(int64) {}, func(int64) {}, func(int64) {})
	if len(sink.events) != 1 || sink.events[0].Name != "settings.read.fail" || sink.events[0].Fields["settings"] != "size-limits" {
		t.Errorf("events = %+v, want one settings.read.fail for size-limits", sink.events)
	}
}
