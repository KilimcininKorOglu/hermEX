// Package antispam scores inbound mail for spam likelihood. It composes sender
// authentication (SPF, DKIM, DMARC), DNS blocklists, a Bayesian content model, and
// a subset of the SpamAssassin rule language into a single verdict. The MTA calls
// it inline at delivery and is fail-open: a scoring error never blocks mail.
//
// The library-backed checks live in checks.go and are injected into Scorer so the
// scoring logic is unit-tested without live DNS.
package antispam

import (
	"fmt"
	"net"
	"strings"

	"golang.org/x/net/publicsuffix"
)

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
	Raw        []byte // the raw RFC 5322 message, for DKIM verification
	ClientIP   net.IP // the connecting SMTP client's IP, for SPF (and later DNSBL)
	HeloName   string // the SMTP HELO/EHLO domain, the SPF fallback when MailFrom has none
	MailFrom   string // the envelope sender, for SPF and DMARC SPF-alignment
	FromDomain string // the From-header domain, for DMARC alignment
}

// Weights assigns each signal's contribution to the spam score. A higher value
// means more spam suspicion; zero disables that signal's contribution.
type Weights struct {
	SPFFail     int
	SPFSoftFail int
	DKIMFail    int // no valid DKIM signature on the message
	DMARCFail   int // DMARC published an enforcing policy and the message did not align
	DNSBLHit    int // the client IP is listed on a DNS blocklist (added per listing zone)
	BayesSpam   int // the Bayesian content model is confident the message is spam
	SARulesHit  int // the SpamAssassin rule subset accumulated enough score (one bounded signal)
}

// DefaultWeights is a conservative starting point; the admin can tune them later.
var DefaultWeights = Weights{SPFFail: 5, SPFSoftFail: 2, DKIMFail: 3, DMARCFail: 6, DNSBLHit: 6, BayesSpam: 4, SARulesHit: 4}

// DefaultThreshold is the score at or above which a message is flagged spam. It
// is deliberately above any single check so one failure alone never condemns a
// message; the admin can tune it later.
const DefaultThreshold = 8

// bayesSpamProb is the spam probability at or above which the Bayesian model
// contributes its weight. It is high so content alone never condemns a message on
// a weak or barely-trained model.
const bayesSpamProb = 0.95

// SAScoreThreshold is the summed SpamAssassin-rule score at or above which the
// rule subset contributes its weight. It matches SpamAssassin's own default
// threshold; since this is only a subset of the full ruleset, requiring the full
// 5.0 from fewer rules is deliberately conservative against false positives.
const SAScoreThreshold = 5.0

// Verdict is the aggregated result for one message.
type Verdict struct {
	Score     int
	Spam      bool
	SPF       AuthResult
	DKIM      AuthResult
	DMARC     AuthResult
	DNSBL     []string // the blocklist zones that listed the client IP
	BayesProb float64  // the Bayesian model's spam probability (0..1); 0 when not run
	SAScore   float64  // the summed score of the SpamAssassin rules that fired; 0 when not run
	SAHits    []string // the names of the SpamAssassin rules that fired
	Reasons   []string
}

// DKIMResult is one verified DKIM signature's claiming domain and validity.
type DKIMResult struct {
	Domain string
	Valid  bool
}

// Scorer computes verdicts. The check functions are injected (New wires the
// production library-backed implementations); tests supply deterministic ones.
type Scorer struct {
	Weights     Weights
	Threshold   int
	Zones       []string    // DNS blocklist zones to query the client IP against; empty disables DNSBL
	Model       *BayesModel // the Bayesian content model; nil leaves content scoring dormant
	SARules     *SARuleSet  // the SpamAssassin rule subset; nil leaves rule scoring dormant
	checkSPF    func(ip net.IP, helo, mailFrom string) AuthResult
	checkDKIM   func(raw []byte) []DKIMResult
	lookupDMARC func(domain string) (policy string, ok bool)
	checkDNSBL  func(ip net.IP, zone string) bool
	extractText func(raw []byte) string
}

// New returns a Scorer wired to the real SPF, DKIM, DMARC, and DNSBL checks,
// flagging a message as spam once its score reaches threshold. DNSBL stays
// dormant until Zones is set, and Bayesian content scoring until Model is set.
func New(w Weights, threshold int) *Scorer {
	return &Scorer{
		Weights: w, Threshold: threshold,
		checkSPF: realSPF, checkDKIM: realDKIM, lookupDMARC: realDMARC, checkDNSBL: realDNSBL,
		extractText: MessageText,
	}
}

// Score runs the configured checks and aggregates a verdict. A check is skipped
// when its inputs are absent, so a partial message still gets a usable result;
// the caller treats scoring as advisory and fail-open.
func (s *Scorer) Score(in Input) Verdict {
	v := Verdict{SPF: AuthNone, DKIM: AuthNone, DMARC: AuthNone}

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

	var validDKIM []string
	if s.checkDKIM != nil && len(in.Raw) > 0 {
		for _, d := range s.checkDKIM(in.Raw) {
			if d.Valid {
				validDKIM = append(validDKIM, d.Domain)
			}
		}
		if len(validDKIM) > 0 {
			v.DKIM = AuthPass
		} else {
			v.DKIM = AuthFail
			v.Score += s.Weights.DKIMFail
			v.Reasons = append(v.Reasons, "no valid DKIM signature")
		}
	}

	// DMARC: the message passes when an authenticated identifier (SPF or DKIM)
	// aligns, under the relaxed organizational-domain rule, with the From domain.
	// Otherwise the domain's published policy decides whether this is a failure.
	if s.lookupDMARC != nil && in.FromDomain != "" {
		policy, ok := s.lookupDMARC(in.FromDomain)
		switch {
		case !ok:
			v.DMARC = AuthNone
		case dmarcAligned(in.FromDomain, in.MailFrom, v.SPF, validDKIM):
			v.DMARC = AuthPass
		default:
			v.DMARC = AuthFail
			if policy == "reject" || policy == "quarantine" {
				v.Score += s.Weights.DMARCFail
				v.Reasons = append(v.Reasons, "DMARC fail (policy "+policy+")")
			}
		}
	}

	// DNSBL: a client IP listed on a configured blocklist zone is a strong signal;
	// each listing zone adds its weight.
	if s.checkDNSBL != nil && in.ClientIP != nil {
		for _, zone := range s.Zones {
			if s.checkDNSBL(in.ClientIP, zone) {
				v.DNSBL = append(v.DNSBL, zone)
				v.Score += s.Weights.DNSBLHit
				v.Reasons = append(v.Reasons, "listed on DNSBL "+zone)
			}
		}
	}

	// Bayesian content score: only a confident spam probability contributes, so a
	// weak or unbootstrapped model never condemns mail on content alone.
	if s.Model != nil && s.extractText != nil && len(in.Raw) > 0 {
		v.BayesProb = s.Model.Score(s.extractText(in.Raw))
		if v.BayesProb >= bayesSpamProb {
			v.Score += s.Weights.BayesSpam
			v.Reasons = append(v.Reasons, "Bayesian: likely spam")
		}
	}

	// SpamAssassin rule subset: the summed score of the rules that fired is one
	// bounded signal — it contributes a single weight once it crosses the SA
	// threshold, however many rules matched, so the subset's score never dominates
	// the verdict on its own.
	if s.SARules != nil && len(in.Raw) > 0 {
		v.SAScore, v.SAHits = s.SARules.Evaluate(in.Raw)
		if v.SAScore >= SAScoreThreshold {
			v.Score += s.Weights.SARulesHit
			v.Reasons = append(v.Reasons, fmt.Sprintf("SpamAssassin rules (score %.1f)", v.SAScore))
		}
	}

	v.Spam = v.Score >= s.Threshold
	return v
}

// dmarcAligned reports whether an authenticated identifier aligns with the From
// domain under DMARC relaxed alignment: a passing SPF on a MailFrom that shares
// the From domain's organizational domain, or a valid DKIM signature whose domain
// does.
func dmarcAligned(fromDomain, mailFrom string, spf AuthResult, validDKIM []string) bool {
	fromOrg := orgDomain(fromDomain)
	if fromOrg == "" {
		return false
	}
	if spf == AuthPass && orgDomain(domainOf(mailFrom)) == fromOrg {
		return true
	}
	for _, d := range validDKIM {
		if orgDomain(d) == fromOrg {
			return true
		}
	}
	return false
}

// orgDomain returns a domain's organizational domain (eTLD+1), the unit DMARC
// relaxed alignment compares. It falls back to the input on a parse failure.
func orgDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	if d == "" {
		return ""
	}
	if e, err := publicsuffix.EffectiveTLDPlusOne(d); err == nil {
		return e
	}
	return d
}

// Tag prepends advisory X-Spam headers to a raw RFC 5322 message reflecting the
// verdict, so a downstream client can filter on it. The original is not modified;
// a new slice is returned. The headers survive the store because oxcmail preserves
// the X-Spam family through its MIME↔MAPI round trip.
func Tag(raw []byte, v Verdict) []byte {
	status, flag := "No", "NO"
	if v.Spam {
		status, flag = "Yes", "YES"
	}
	hdr := fmt.Sprintf("X-Spam-Flag: %s\r\nX-Spam-Score: %d\r\nX-Spam-Status: %s, score=%d\r\n",
		flag, v.Score, status, v.Score)
	out := make([]byte, 0, len(hdr)+len(raw))
	out = append(out, hdr...)
	out = append(out, raw...)
	return out
}

// domainOf extracts the lowercased domain from an address, or "" when it has none.
func domainOf(addr string) string {
	if _, dom, ok := strings.Cut(strings.ToLower(strings.TrimSpace(addr)), "@"); ok {
		return dom
	}
	return ""
}

// ParseZones splits a comma-separated list of DNS blocklist zones into a clean
// slice, dropping blanks and surrounding whitespace.
func ParseZones(s string) []string {
	var zones []string
	for z := range strings.SplitSeq(s, ",") {
		if z = strings.TrimSpace(z); z != "" {
			zones = append(zones, z)
		}
	}
	return zones
}
