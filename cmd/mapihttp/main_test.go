package main

import (
	"errors"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/logging"
)

// recordingSetters captures what applyMapiSizeLimit hands each cap; -1 means the
// setter was never called.
func recordingSetters() (*[2]int64, mapiSizeSetters) {
	got := &[2]int64{-1, -1}
	return got, mapiSizeSetters{
		requestBody:   func(n int64) { got[0] = n },
		uploadMessage: func(n int64) { got[1] = n },
	}
}

// TestApplyMapiSizeLimit proves both MAPI caps, the request body and the uploaded
// message stream, are applied from their own stored fields on a clean read, and
// that a read error or a missing row leaves both untouched, so a transient
// directory failure never shrinks a running cap to zero.
func TestApplyMapiSizeLimit(t *testing.T) {
	got, set := recordingSetters()
	applyMapiSizeLimit(nil,
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{MapiRequestBytes: 8192, MapiUploadMessageBytes: 65536}, true, nil
		}, set)
	if got[0] != 8192 || got[1] != 65536 {
		t.Errorf("applied caps = request %d, upload %d, want 8192 and 65536", got[0], got[1])
	}

	// found is true beside the error, so only the error guard stands between a
	// half-read row and the setters: the !found guard cannot hide its loss.
	got, set = recordingSetters()
	applyMapiSizeLimit(nil,
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{MapiRequestBytes: 8192, MapiUploadMessageBytes: 65536}, true, errors.New("db down")
		}, set)
	if *got != [2]int64{-1, -1} {
		t.Errorf("a setter was called on a read error (%v); the caps must be left unchanged", *got)
	}

	got, set = recordingSetters()
	applyMapiSizeLimit(nil,
		func() (directory.SizeLimits, bool, error) { return directory.SizeLimits{}, false, nil }, set)
	if *got != [2]int64{-1, -1} {
		t.Errorf("a setter was called with no stored row (%v); the defaults must stand", *got)
	}
}

// captureSink collects the events an applier emits.
type captureSink struct{ events []logging.Event }

func (s *captureSink) Write(e logging.Event) { s.events = append(s.events, e) }

// TestApplyMapiSizeLimitRecordsAReadFailure proves the read failure the applier
// swallows to keep the running caps is still recorded in the central log, the
// operator's only view of a settings row that stopped being read.
func TestApplyMapiSizeLimitRecordsAReadFailure(t *testing.T) {
	sink := &captureSink{}
	_, set := recordingSetters()
	applyMapiSizeLimit(logging.New(sink),
		func() (directory.SizeLimits, bool, error) {
			return directory.SizeLimits{}, false, errors.New("db down")
		}, set)
	if len(sink.events) != 1 || sink.events[0].Name != "settings.read.fail" || sink.events[0].Fields["settings"] != "size-limits" {
		t.Errorf("events = %+v, want one settings.read.fail for size-limits", sink.events)
	}
}
