package main

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/emersion/go-msgauth/dmarc"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/mta"
	"hermex/internal/relay"
)

// dmarcLookupTimeout bounds one DNS query the DMARC report pass makes. The names
// belong to other domains, and the pass runs under the relay drain guard.
const dmarcLookupTimeout = 5 * time.Second

// dmarcSwitch holds the operator's DMARC aggregate reporting setting as last read.
// It starts off, the setting's default, until the first read turns it on.
type dmarcSwitch struct{ on atomic.Bool }

// Enabled reports the setting as last read.
func (s *dmarcSwitch) Enabled() bool { return s.on.Load() }

// gatedDMARC counts messages for the DMARC aggregate reports only while reporting
// is on, so nothing about the senders of received mail is kept when it is off.
type gatedDMARC struct {
	sw  *dmarcSwitch
	rec mta.DMARCRecorder
}

// RecordDMARC passes the observation on while reporting is on.
func (g gatedDMARC) RecordDMARC(now time.Time, o relay.DMARCObservation) error {
	if !g.sw.Enabled() {
		return nil
	}
	return g.rec.RecordDMARC(now, o)
}

// applyDMARCReportSetting reads the stored setting and applies it. A read error
// leaves the switch as it is and is recorded; a missing row means off.
func applyDMARCReportSetting(logger *logging.Logger, read func() (directory.DMARCReportSettings, bool, error), set func(bool)) {
	s, _, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, daemonName, "dmarc-reports", "leaving DMARC reporting unchanged", err)
		return
	}
	set(s.Enabled)
}

// runDMARCReportMaintenance re-applies the setting every minute so an operator's
// change takes effect without a restart. It runs until the process exits.
func runDMARCReportMaintenance(logger *logging.Logger, read func() (directory.DMARCReportSettings, bool, error), set func(bool)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyDMARCReportSetting(logger, read, set)
	}
}

// lookupDMARCRecord fetches a policy domain's DMARC record for its report.
func lookupDMARCRecord(domain string) (*dmarc.Record, error) {
	return dmarc.LookupWithOptions(domain, &dmarc.LookupOptions{LookupTXT: lookupTXT})
}

// lookupTXT resolves TXT records under the report pass's deadline.
func lookupTXT(name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dmarcLookupTimeout)
	defer cancel()
	return net.DefaultResolver.LookupTXT(ctx, name)
}
