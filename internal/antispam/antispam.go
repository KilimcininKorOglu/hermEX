// Package antispam scores inbound mail for spam likelihood. It composes sender
// authentication (SPF, DKIM, and — in a later increment — DMARC), DNS blocklists,
// and a Bayesian content model into a single verdict. The MTA calls it inline at
// delivery and is fail-open: a scoring error never blocks mail.
//
// The library-backed checks live in checks.go and are injected into Scorer so the
// scoring logic is unit-tested without live DNS.
package antispam

import "net"

// AuthResult is the outcome of one sender-authentication check.
type AuthResult string

const (
	AuthPass     AuthResult = "pass"
	AuthFail     AuthResult = "fail"
	AuthSoftFail AuthResult = "softfail"
	AuthNeutral  AuthResult = "neutral"
	AuthNone     AuthResult = "none"
	AuthError    AuthResult = "error"
)

// Input is everything the scorer needs about one inbound message.
type Input struct {
	Raw      []byte // the raw RFC 5322 message, for DKIM verification
	ClientIP net.IP // the connecting SMTP client's IP, for SPF (and later DNSBL)
	HeloName string // the SMTP HELO/EHLO domain, the SPF fallback when MailFrom has none
	MailFrom string // the envelope sender, for SPF
}

// Weights assigns each signal's contribution to the spam score. A higher value
// means more spam suspicion; zero disables that signal's contribution.
type Weights struct {
	SPFFail     int
	SPFSoftFail int
	DKIMFail    int // no valid DKIM signature on the message
}

// DefaultWeights is a conservative starting point; the admin can tune them later.
var DefaultWeights = Weights{SPFFail: 5, SPFSoftFail: 2, DKIMFail: 3}

// Verdict is the aggregated result for one message.
type Verdict struct {
	Score   int
	Spam    bool
	SPF     AuthResult
	DKIM    AuthResult
	Reasons []string
}

// DKIMResult is one verified DKIM signature's claiming domain and validity.
type DKIMResult struct {
	Domain string
	Valid  bool
}

// Scorer computes verdicts. checkSPF and checkDKIM are injected (New wires the
// production library-backed implementations); tests supply deterministic ones.
type Scorer struct {
	Weights   Weights
	Threshold int
	checkSPF  func(ip net.IP, helo, mailFrom string) AuthResult
	checkDKIM func(raw []byte) []DKIMResult
}

// New returns a Scorer wired to the real SPF and DKIM libraries, flagging a
// message as spam once its score reaches threshold.
func New(w Weights, threshold int) *Scorer {
	return &Scorer{Weights: w, Threshold: threshold, checkSPF: realSPF, checkDKIM: realDKIM}
}

// Score runs the configured checks and aggregates a verdict. A check is skipped
// when its inputs are absent (e.g. no client IP), so a partial message still
// gets a usable result; the caller treats scoring as advisory and fail-open.
func (s *Scorer) Score(in Input) Verdict {
	v := Verdict{SPF: AuthNone, DKIM: AuthNone}

	if s.checkSPF != nil && in.ClientIP != nil && in.MailFrom != "" {
		v.SPF = s.checkSPF(in.ClientIP, in.HeloName, in.MailFrom)
		switch v.SPF {
		case AuthFail:
			v.Score += s.Weights.SPFFail
			v.Reasons = append(v.Reasons, "SPF fail")
		case AuthSoftFail:
			v.Score += s.Weights.SPFSoftFail
			v.Reasons = append(v.Reasons, "SPF softfail")
		}
	}

	if s.checkDKIM != nil && len(in.Raw) > 0 {
		valid := false
		for _, d := range s.checkDKIM(in.Raw) {
			if d.Valid {
				valid = true
				break
			}
		}
		if valid {
			v.DKIM = AuthPass
		} else {
			v.DKIM = AuthFail
			v.Score += s.Weights.DKIMFail
			v.Reasons = append(v.Reasons, "no valid DKIM signature")
		}
	}

	v.Spam = v.Score >= s.Threshold
	return v
}
