package tlscert

import (
	"context"
	"fmt"
	"time"

	"hermex/internal/health"
)

// expiryWarning is how long before a serving certificate lapses that the health
// check starts reporting the daemon as degraded. Automated issuance renews with
// about a month to spare, so a warning this late means renewal has actually
// failed and an operator has to act, not that the clock is simply ticking.
const expiryWarning = 14 * 24 * time.Hour

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
