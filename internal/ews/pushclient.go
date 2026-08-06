package ews

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"

	"hermex/internal/ssrfguard"
)

// Push-callback delivery tuning. The transport's own timeouts live in
// internal/ssrfguard, which builds the client.
const (
	pushMaxFailures  = 5 // consecutive POST failures before the subscription is dropped
	pushMaxRespBytes = 64 << 10
)

// sendNotificationResultEnvelope extracts the SubscriptionStatus the client returns
// from its callback. The path matches regardless of namespace prefixes (Go's xml
// path matching uses local names).
type sendNotificationResultEnvelope struct {
	XMLName xml.Name
	Status  string `xml:"Body>SendNotificationResult>SubscriptionStatus"`
}

// deliverPush POSTs body to the callback URL and reports whether to keep the
// subscription. A transport error (including an SSRF-guard refusal) is returned so
// the caller can count it toward the failure budget; a parsed SubscriptionStatus of
// "Unsubscribe" returns keep=false with no error (the client asked to stop). A
// missing or "OK" status keeps the subscription.
func (s *Server) deliverPush(callbackURL string, body []byte) (keep bool, err error) {
	req, err := http.NewRequest(http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	resp, err := s.pushHTTP.Do(req)
	if err != nil {
		return true, err // transient/guard failure: count it, do not unsubscribe yet
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return true, fmt.Errorf("ews push: callback returned status %d", resp.StatusCode)
	}
	var env sendNotificationResultEnvelope
	if err := xml.NewDecoder(io.LimitReader(resp.Body, pushMaxRespBytes)).Decode(&env); err != nil {
		return true, nil // unparseable but delivered: keep the subscription
	}
	return !strings.EqualFold(env.Status, "Unsubscribe"), nil
}

// pushClient builds the SSRF-guarded client for push callbacks. The URL a client
// names in a Subscribe request is attacker-controlled, so the callback never uses a
// bare http.Client.
func pushClient(allowInternal bool) *http.Client { return ssrfguard.Client(allowInternal) }

// validateCallbackURL is the scheme gate applied to a Subscribe request's callback
// URL, before it is ever stored or dialed.
func validateCallbackURL(raw string, allowHTTP bool) error {
	return ssrfguard.ValidateURL(raw, allowHTTP)
}
