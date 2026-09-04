package relay

import (
	"strings"
	"testing"
)

// TestReportDeliveryRefusesWithoutGuardedClient is the latent hardening defect. The
// report target is taken from the destination domain's own DNS record, so every
// safety property of the POST lives in the injected client: its timeout, its refusal
// to follow redirects, and its dial-time address check. Falling back to the default
// client kept the attacker-supplied URL and dropped all three, which would let a
// slow or internal target hold the delivery goroutine open or be reached at all.
func TestReportDeliveryRefusesWithoutGuardedClient(t *testing.T) {
	w := &Worker{} // TLSHTTPClient deliberately unset

	err := w.deliverReportHTTPS("https://report.example/collector", []byte(`{"report":1}`))
	if err == nil {
		t.Fatal("delivery with no guarded client was attempted, want a refusal")
	}
	if !strings.Contains(err.Error(), "guarded HTTP client") {
		t.Errorf("refusal = %v, want it to name the missing guarded client", err)
	}
}
