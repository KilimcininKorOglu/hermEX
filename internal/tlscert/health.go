package tlscert

import (
	"context"
	"fmt"
	"time"

	"hermex/internal/health"
	"hermex/internal/logging"
)

// expiryWarning is how long before a serving certificate lapses that the health
// check starts reporting the daemon as degraded. Automated issuance renews with
// about a month to spare, so a warning this late means renewal has actually
// failed and an operator has to act, not that the clock is simply ticking.
const expiryWarning = 14 * 24 * time.Hour

// expiryNoticeInterval throttles the warning the certificate poll emits. The poll
// runs every pollInterval and an expiring certificate keeps expiring, so an
// unthrottled notice would repeat thousands of times a day and bury everything
// else in the log. Six hours puts several notices a day in front of an operator
// reading the log sink while keeping the volume negligible.
const expiryNoticeInterval = 6 * time.Hour

// noteExpiry reports an approaching or passed expiry to the central log, at most
// once per expiryNoticeInterval. It is the push half of the expiry reaction:
// ExpiryCheck judges the same condition, but only answers a caller that polls
// /healthz, which is opt-in and not enabled by default, so on a deployment that
// never enables it nothing about a failed renewal reaches the operator until
// clients start failing. Only RunMaintenance calls this, so the throttle stamp
// needs no synchronization.
func (p *Provider) noteExpiry(now time.Time) {
	if p.logger == nil {
		return
	}
	notAfter, ok := p.Expiry()
	if !ok {
		return // nothing served over TLS, nothing to judge
	}
	left := notAfter.Sub(now)
	if left >= expiryWarning {
		return
	}
	if !p.lastExpiryNotice.IsZero() && now.Sub(p.lastExpiryNotice) < expiryNoticeInterval {
		return
	}
	p.lastExpiryNotice = now
	f := logging.Fields{"expires": notAfter.UTC().Format(time.RFC3339)}
	if left <= 0 {
		p.logger.Error(logging.TLS, "certificate.expired", f)
		return
	}
	f["days_left"] = int(left / (24 * time.Hour))
	p.logger.Warn(logging.TLS, "certificate.expiring", f)
}

// ExpiryCheck builds the readiness probe that reports the serving certificate's
// remaining validity. It fails once the certificate has lapsed (clients can no
// longer connect) and also while it is inside the warning window (renewal has not
// happened and someone must intervene). A daemon adds it to health.Components
// only when TLS is on, so the check never claims to have judged a certificate
// that does not exist.
//
// The daemons' /healthz is read by the admin Live status page alone, so a failing
// probe surfaces as "degraded" in the panel; it does not move traffic.
func ExpiryCheck(p *Provider) health.Check {
	return health.Check{
		Name: "tls-certificate",
		Probe: func(context.Context) error {
			notAfter, ok := p.Expiry()
			if !ok {
				return nil // nothing served over TLS, nothing to judge
			}
			day := notAfter.UTC().Format("2006-01-02")
			left := time.Until(notAfter)
			switch {
			case left <= 0:
				return fmt.Errorf("certificate expired on %s", day)
			case left < expiryWarning:
				return fmt.Errorf("certificate expires on %s, %d days left", day, int(left/(24*time.Hour)))
			}
			return nil
		},
	}
}
