package main

import (
	"errors"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/relay"
)

// countingDMARC counts the observations that reach the spool.
type countingDMARC struct{ n int }

func (c *countingDMARC) RecordDMARC(time.Time, relay.DMARCObservation) error {
	c.n++
	return nil
}

// TestDMARCSwitchGatesCounting proves the operator setting, as the poll applies it,
// decides whether a message is counted: off by default, on after a read that says
// so, unchanged after a failed read, and off again after a read that says so.
func TestDMARCSwitchGatesCounting(t *testing.T) {
	sw := &dmarcSwitch{}
	spool := &countingDMARC{}
	g := gatedDMARC{sw: sw, rec: spool}
	record := func() {
		if err := g.RecordDMARC(time.Now(), relay.DMARCObservation{}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(on bool, err error) func() (directory.DMARCReportSettings, bool, error) {
		return func() (directory.DMARCReportSettings, bool, error) {
			return directory.DMARCReportSettings{Enabled: on}, err == nil, err
		}
	}

	record()
	wantEqInt(t, spool.n, 0, "counted before any read")

	applyDMARCReportSetting(nil, read(true, nil), sw.on.Store)
	record()
	wantEqInt(t, spool.n, 1, "counted while on")

	sink := &sweepSink{}
	applyDMARCReportSetting(logging.New(sink), read(false, errors.New("db down")), sw.on.Store)
	if !sw.Enabled() {
		t.Error("a failed read turned reporting off; it must leave the switch unchanged")
	}
	if _, ok := sink.find("settings.read.fail"); !ok {
		t.Error("the failed read emitted no settings.read.fail event")
	}

	applyDMARCReportSetting(nil, read(false, nil), sw.on.Store)
	record()
	wantEqInt(t, spool.n, 1, "counted after switching off")
}

func wantEqInt(t *testing.T, got, want int, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", what, got, want)
	}
}
