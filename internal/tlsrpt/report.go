package tlsrpt

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// STARTTLS validation result types (RFC 8460 §4.3, §6.6 registry). A successful
// session carries no result type; these name the ways a session can fail.
const (
	ResultStartTLSNotSupported    = "starttls-not-supported"
	ResultCertificateHostMismatch = "certificate-host-mismatch"
	ResultCertificateExpired      = "certificate-expired"
	ResultTLSAInvalid             = "tlsa-invalid"
	ResultDNSSECInvalid           = "dnssec-invalid"
	ResultDANERequired            = "dane-required"
	ResultCertificateNotTrusted   = "certificate-not-trusted"
	ResultSTSPolicyInvalid        = "sts-policy-invalid"
	ResultSTSWebPKIInvalid        = "sts-webpki-invalid"
	ResultValidationFailure       = "validation-failure"
)

// Policy types (RFC 8460 §4.4): the kind of TLS policy a set of sessions was
// evaluated against. "no-policy-found" covers opportunistic delivery.
const (
	PolicyTypeTLSA     = "tlsa"
	PolicyTypeSTS      = "sts"
	PolicyTypeNoPolicy = "no-policy-found"
)

// Report is a daily aggregate TLS report (RFC 8460 §4.4), the I-JSON document a
// sender delivers to a recipient domain's rua address.
type Report struct {
	OrganizationName string         `json:"organization-name"`
	DateRange        DateRange      `json:"date-range"`
	ContactInfo      string         `json:"contact-info"`
	ReportID         string         `json:"report-id"`
	Policies         []PolicyResult `json:"policies"`
}

// DateRange is the UTC interval a report covers (RFC 8460 §4.4): a full day,
// start inclusive to end inclusive, in RFC 3339 date-time form.
type DateRange struct {
	Start time.Time `json:"start-datetime"`
	End   time.Time `json:"end-datetime"`
}

// PolicyResult ties one evaluated policy to its session outcome counts.
type PolicyResult struct {
	Policy         PolicyDescriptor `json:"policy"`
	Summary        Summary          `json:"summary"`
	FailureDetails []FailureDetail  `json:"failure-details,omitempty"`
}

// PolicyDescriptor identifies the policy a block of sessions was evaluated
// against (RFC 8460 §4.4). PolicyString and MXHost are optional and omitted when
// empty (a reconstructed counter need not carry the verbatim policy text).
type PolicyDescriptor struct {
	PolicyType   string   `json:"policy-type"`
	PolicyString []string `json:"policy-string,omitempty"`
	PolicyDomain string   `json:"policy-domain"`
	MXHost       string   `json:"mx-host,omitempty"`
}

// UnmarshalJSON reads mx-host in both forms reporters send. RFC 8460 §4.4
// describes it as a JSON array of strings, while the report example in its
// Appendix B writes one string. Several patterns are joined with ", ". Encoding
// is unchanged: this server writes the single-string form.
func (p *PolicyDescriptor) UnmarshalJSON(b []byte) error {
	type plain PolicyDescriptor
	var v struct {
		plain
		MXHost json.RawMessage `json:"mx-host"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	host, err := mxHostPatterns(v.MXHost)
	if err != nil {
		return err
	}
	*p = PolicyDescriptor(v.plain)
	p.MXHost = host
	return nil
}

// mxHostPatterns reads an mx-host value written as a string or as an array of
// strings. An absent or null value is empty.
func mxHostPatterns(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return one, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return "", fmt.Errorf("tlsrpt: mx-host is neither a string nor an array of strings: %w", err)
	}
	return strings.Join(many, ", "), nil
}

// Summary is the per-policy session tally (RFC 8460 §4.4).
type Summary struct {
	TotalSuccessful int `json:"total-successful-session-count"`
	TotalFailure    int `json:"total-failure-session-count"`
}

// FailureDetail breaks one failure result type down by count and the connection
// facts a recipient needs to diagnose it (RFC 8460 §4.4). The IP and reason-code
// fields are optional and omitted when unknown.
type FailureDetail struct {
	ResultType          string `json:"result-type"`
	SendingMTAIP        string `json:"sending-mta-ip,omitempty"`
	ReceivingMXHostname string `json:"receiving-mx-hostname,omitempty"`
	ReceivingIP         string `json:"receiving-ip,omitempty"`
	FailedSessionCount  int    `json:"failed-session-count"`
	FailureReasonCode   string `json:"failure-reason-code,omitempty"`
}

// JSON renders the report as the I-JSON document defined by RFC 8460 §4.4.
func (r *Report) JSON() ([]byte, error) {
	return json.Marshal(r)
}
