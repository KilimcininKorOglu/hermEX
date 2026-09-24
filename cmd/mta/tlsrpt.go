package main

import (
	"log"
	"net/http"
	"time"

	"hermex/internal/directory"
	"hermex/internal/httplimit"
	"hermex/internal/lifecycle"
	"hermex/internal/logging"
	"hermex/internal/mta"
	"hermex/internal/serve"
	"hermex/internal/tlscert"
)

// reportEndpoint builds the HTTP listener that accepts TLS reports posted over
// HTTPS (RFC 8460 §3), which the gateway reaches at /tlsrpt. It is off when
// mta_http_addr is empty. The listener serves the same certificate as SMTP, so
// the gateway's hop to it is encrypted when TLS is on.
func (d *mtaDaemon) reportEndpoint(provider *tlscert.Provider) []lifecycle.Component {
	addr := d.cfg.MTAHTTPAddr
	if addr == "" {
		return nil
	}
	h := mta.NewTLSReportHandler(d.dir, d.logger)
	// The body cap: read at startup and re-read every minute, so an operator's
	// change applies without a restart. The built-in cap holds until a row exists.
	applyTLSReportLimit(d.logger, d.dir.GetSizeLimits, h.SetMaxBodyBytes)
	go runTLSReportLimitMaintenance(d.logger, d.dir.GetSizeLimits, h.SetMaxBodyBytes)
	// Per-client request limiter, as every other HTTP daemon runs it: off until an
	// operator enables it, and a read failure leaves it as it is.
	limiter := httplimit.NewLimiter()
	httplimit.Apply(daemonName, d.logger, limiter, d.dir.GetHTTPRateLimitSettings)
	go httplimit.RunMaintenance(daemonName, d.logger, limiter, d.dir.GetHTTPRateLimitSettings)
	mux := http.NewServeMux()
	mux.Handle("/tlsrpt", h)
	mux.Handle("/tlsrpt/", h)
	hs, err := serve.New(addr, mux, provider, d.logger, logging.MTA, limiter)
	if err != nil {
		log.Fatalf("hermex-mta: TLS report endpoint on %s: %v", addr, err)
	}
	log.Printf("hermex-mta accepting TLS reports on %s", addr)
	return []lifecycle.Component{hs}
}

// applyTLSReportLimit reads the stored TLS report body cap and applies it. A
// missing row or a read error leaves the cap unchanged, so a settings failure
// never lifts or shrinks it unexpectedly.
func applyTLSReportLimit(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setMax func(int64)) {
	s, found, err := read()
	if err != nil {
		logging.SettingsReadFailed(logger, daemonName, "size-limits", "leaving the TLS report cap unchanged", err)
		return
	}
	if !found {
		return
	}
	setMax(s.TLSReportBytes)
}

// runTLSReportLimitMaintenance re-applies the cap every minute so an admin change
// takes effect without a restart. It runs until the process exits.
func runTLSReportLimitMaintenance(logger *logging.Logger, read func() (directory.SizeLimits, bool, error), setMax func(int64)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for range tick.C {
		applyTLSReportLimit(logger, read, setMax)
	}
}
