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
	"sync/atomic"

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

// DefaultBayesProb is the spam probability at or above which the Bayesian model
// contributes its weight. It is high so content alone never condemns a message on
// a weak or barely-trained model.
const DefaultBayesProb = 0.95

// DefaultSAThreshold is the summed SpamAssassin-rule score at or above which the
// rule subset contributes its weight. It matches SpamAssassin's own default
// threshold; since this is only a subset of the full ruleset, requiring the full
// 5.0 from fewer rules is deliberately conservative against false positives.
const DefaultSAThreshold = 5.0

// Verdict is the aggregated result for one message.
type Verdict struct {
	Score int
	Spam  bool
	// AccessMatched reports that an operator allow/block rule decided this verdict
	// (so Spam is authoritative at the message level and must not be re-evaluated
	// against a per-recipient threshold). It is false for a purely score-driven
	// verdict.
	AccessMatched bool
	// AccessAction is the operator allow/block action that matched (AccessAllow,
	// AccessBlock, or "" for none). Delivery needs the action itself, not just that
	// one matched, to honor per-recipient precedence: an operator block is absolute
	// (a recipient's own allow cannot rescue it) while an operator allow may be
	// narrowed by a recipient's own block.
	AccessAction string
	SPF          AuthResult
	DKIM         AuthResult
	// DKIMDomains are the d= domains of the signatures that verified, in message
	// order. A caller that needs to know who vouched for a message reads them here
	// instead of verifying the signatures a second time.
	DKIMDomains []string
	DMARC       AuthResult
	// DMARCReject reports a DMARC failure under an enforcing policy (reject or
	// quarantine), the strongest spoofing signal. No allow rule, operator or
	// per-recipient, may rescue such a message from the score-based verdict.
	DMARCReject bool
	DNSBL       []string // the blocklist zones that listed the client IP
	BayesProb   float64  // the Bayesian model's spam probability (0..1); 0 when not run
	SAScore     float64  // the summed score of the SpamAssassin rules that fired; 0 when not run
	SAHits      []string // the names of the SpamAssassin rules that fired
	Reasons     []string
}

// DKIMResult is one verified DKIM signature's claiming domain and validity.
type DKIMResult struct {
	Domain string
	Valid  bool
}

// Config is the Scorer's hot-swappable tuning: the signal weights, the spam
// threshold, the DNS blocklist zones, and the Bayes/SpamAssassin cutoffs. It is
// swapped as one unit so Score always observes a coherent snapshot (never new
// weights with an old threshold).
type Config struct {
	Weights   Weights
	Threshold int
	Zones     []string // DNS blocklist zones to query the client IP against; empty disables DNSBL
	// BayesProb is the spam-probability cutoff at or above which the Bayes weight is
	// applied; SAThreshold is the summed SpamAssassin-rule score at or above which the
	// SA-rules weight is applied. A value <= 0 falls back to the built-in default
	// (DefaultBayesProb / DefaultSAThreshold), so an unset or partial Config scores as before.
	BayesProb   float64
	SAThreshold float64
}

// Scorer computes verdicts. The check functions are injected (New wires the
// production library-backed implementations); tests supply deterministic ones.
type Scorer struct {
	// cfg (weights, threshold, zones), model, and saRules are held behind atomic
	// pointers so the MTA can hot-swap edited settings, a retrained model, or a
	// refreshed ruleset while Score runs concurrently, without a restart. A nil
	// model/saRules leaves that signal dormant. Set them via SetConfig, SetModel,
	// and SetRules.
	cfg         atomic.Pointer[Config]
	model       atomic.Pointer[BayesModel]
	saRules     atomic.Pointer[SARuleSet]
	access      atomic.Pointer[AccessList]
	checkSPF    func(ip net.IP, helo, mailFrom string) AuthResult
	checkDKIM   func(raw []byte) []DKIMResult
	lookupDMARC func(domain string) (policy string, ok bool)
	checkDNSBL  func(ip net.IP, zone string) bool
	extractText func(raw []byte) string
}

// SetConfig installs (or replaces) the weights, threshold, and blocklist zones. It
// is safe to call concurrently with Score, so edited settings apply without a
// restart.
func (s *Scorer) SetConfig(c *Config) { s.cfg.Store(c) }

// SetModel installs (or replaces) the Bayesian content model. It is safe to call
// concurrently with Score, so a retrained model can be hot-swapped in without a
// restart; a nil model leaves content scoring dormant.
func (s *Scorer) SetModel(m *BayesModel) { s.model.Store(m) }

// SetRules installs (or replaces) the SpamAssassin ruleset. It is safe to call
// concurrently with Score, so a refreshed ruleset applies without a restart; a
// nil ruleset leaves rule scoring dormant.
func (s *Scorer) SetRules(rs *SARuleSet) { s.saRules.Store(rs) }

// SetAccess installs (or replaces) the operator allow/block rules. It is safe to
// call concurrently with Score, so edited rules apply without a restart; a nil
// list leaves the verdict unoverridden.
func (s *Scorer) SetAccess(a *AccessList) { s.access.Store(a) }

// Allowlisted reports whether the sender is on the operator allow list, so a caller
// (the greylister) can skip a redundant check for a trusted sender. It reads the
// same hot-reloaded list Score consults.
func (s *Scorer) Allowlisted(mailFrom, fromDomain string) bool {
	acc := s.access.Load()
	return acc != nil && acc.Action(mailFrom, fromDomain) == AccessAllow
}

// New returns a Scorer wired to the real SPF, DKIM, DMARC, and DNSBL checks,
// flagging a message as spam once its score reaches threshold. DNSBL stays
// dormant until SetConfig supplies zones, Bayesian content scoring until SetModel
// is called, and SpamAssassin rules until SetRules is called.
func New(w Weights, threshold int) *Scorer {
	s := &Scorer{
		checkSPF: realSPF, checkDKIM: realDKIM, lookupDMARC: realDMARC, checkDNSBL: realDNSBL,
		extractText: MessageText,
	}
	s.cfg.Store(&Config{Weights: w, Threshold: threshold})
	return s
}

// Score runs the configured checks and aggregates a verdict. A check is skipped
// when its inputs are absent, so a partial message still gets a usable result;
// the caller treats scoring as advisory and fail-open.
func (s *Scorer) Score(in Input) Verdict {
	v := Verdict{SPF: AuthNone, DKIM: AuthNone, DMARC: AuthNone}
	cfg := s.scoringConfig()

	s.scoreSPF(&v, in, cfg)
	validDKIM := s.scoreDKIM(&v, in, cfg)
	s.scoreDMARC(&v, in, cfg, validDKIM)
	s.scoreDNSBL(&v, in, cfg)
	s.scoreContent(&v, in, cfg)

	v.Spam = v.Score >= cfg.Threshold
	s.applyAccess(&v, in)
	return v
}

// scoringConfig loads the tuning once so the whole verdict uses one coherent
// snapshot even if settings are hot-swapped mid-scoring, and fills in the
// built-in defaults for whatever an unconfigured or partially-set Config leaves
// unset, so it scores as before.
func (s *Scorer) scoringConfig() *Config {
	cfg := s.cfg.Load()
	if cfg == nil {
		cfg = &Config{Weights: DefaultWeights, Threshold: DefaultThreshold}
	}
	out := *cfg
	if out.BayesProb <= 0 {
		out.BayesProb = DefaultBayesProb
	}
	if out.SAThreshold <= 0 {
		out.SAThreshold = DefaultSAThreshold
	}
	return &out
}

// scoreSPF checks the envelope sender against the client's address.
func (s *Scorer) scoreSPF(v *Verdict, in Input, cfg *Config) {
	if s.checkSPF == nil || in.ClientIP == nil || in.MailFrom == "" {
		return
	}
	v.SPF = s.checkSPF(in.ClientIP, in.HeloName, in.MailFrom)
	switch v.SPF {
	case AuthFail:
		v.Score += cfg.Weights.SPFFail
		v.Reasons = append(v.Reasons, "SPF fail")
	case AuthSoftFail:
		v.Score += cfg.Weights.SPFSoftFail
		v.Reasons = append(v.Reasons, "SPF softfail")
	}
}

// scoreDKIM verifies the signatures and returns the domains that signed validly,
// which DMARC alignment is then evaluated against.
func (s *Scorer) scoreDKIM(v *Verdict, in Input, cfg *Config) []string {
	if s.checkDKIM == nil || len(in.Raw) == 0 {
		return nil
	}
	var validDKIM []string
	for _, d := range s.checkDKIM(in.Raw) {
		if d.Valid {
			validDKIM = append(validDKIM, d.Domain)
		}
	}
	if len(validDKIM) > 0 {
		v.DKIM = AuthPass
		v.DKIMDomains = validDKIM
		return validDKIM
	}
	v.DKIM = AuthFail
	v.Score += cfg.Weights.DKIMFail
	v.Reasons = append(v.Reasons, "no valid DKIM signature")
	return nil
}

// scoreDMARC records whether an authenticated identifier (SPF or DKIM) aligns,
// under the relaxed organizational-domain rule, with the From domain. Otherwise
// the domain's published policy decides whether this is a failure. DMARCReject
// records a failure under an enforcing policy, the strongest spoofing signal, so
// an allowlist override cannot rescue a spoofed sender.
func (s *Scorer) scoreDMARC(v *Verdict, in Input, cfg *Config, validDKIM []string) {
	if s.lookupDMARC == nil || in.FromDomain == "" {
		return
	}
	policy, ok := s.lookupDMARC(in.FromDomain)
	switch {
	case !ok:
		v.DMARC = AuthNone
	case dmarcAligned(in.FromDomain, in.MailFrom, v.SPF, validDKIM):
		v.DMARC = AuthPass
	default:
		v.DMARC = AuthFail
		if policy == "reject" || policy == "quarantine" {
			v.Score += cfg.Weights.DMARCFail
			v.Reasons = append(v.Reasons, "DMARC fail (policy "+policy+")")
			v.DMARCReject = true
		}
	}
}

// scoreDNSBL adds a weight per blocklist zone listing the client address, a
// strong signal.
func (s *Scorer) scoreDNSBL(v *Verdict, in Input, cfg *Config) {
	if s.checkDNSBL == nil || in.ClientIP == nil {
		return
	}
	for _, zone := range cfg.Zones {
		if s.checkDNSBL(in.ClientIP, zone) {
			v.DNSBL = append(v.DNSBL, zone)
			v.Score += cfg.Weights.DNSBLHit
			v.Reasons = append(v.Reasons, "listed on DNSBL "+zone)
		}
	}
}

// scoreContent adds the two content signals: the Bayesian probability, where only
// a confident spam score contributes so a weak or unbootstrapped model never
// condemns mail on content alone, and the SpamAssassin rule subset, whose summed
// score contributes one weight once it crosses the SA threshold, however many
// rules matched, so the subset never dominates the verdict on its own.
func (s *Scorer) scoreContent(v *Verdict, in Input, cfg *Config) {
	if len(in.Raw) == 0 {
		return
	}
	if m := s.model.Load(); m != nil && s.extractText != nil {
		v.BayesProb = m.Score(s.extractText(in.Raw))
		if v.BayesProb >= cfg.BayesProb {
			v.Score += cfg.Weights.BayesSpam
			v.Reasons = append(v.Reasons, "Bayesian: likely spam")
		}
	}
	if rs := s.saRules.Load(); rs != nil {
		v.SAScore, v.SAHits = rs.Evaluate(in.Raw)
		if v.SAScore >= cfg.SAThreshold {
			v.Score += cfg.Weights.SARulesHit
			v.Reasons = append(v.Reasons, fmt.Sprintf("SpamAssassin rules (score %.1f)", v.SAScore))
		}
	}
}

// applyAccess lets the operator's allow/block rules override the verdict last. A
// blocklisted sender is always spam; an allowlisted sender is rescued from
// score-based junking, but a hard DMARC failure (a spoofing signal) still wins so
// an allowlisted domain cannot be abused to bypass authentication. An empty
// MailFrom (a bounce) is never matched.
func (s *Scorer) applyAccess(v *Verdict, in Input) {
	acc := s.access.Load()
	if acc == nil || in.MailFrom == "" {
		return
	}
	action := acc.Action(in.MailFrom, in.FromDomain)
	v.AccessMatched = action != ""
	v.AccessAction = action
	switch action {
	case AccessBlock:
		v.Spam = true
		v.Reasons = append(v.Reasons, "blocklisted sender")
	case AccessAllow:
		if v.DMARCReject {
			v.Reasons = append(v.Reasons, "allowlisted sender (overridden by DMARC failure)")
			return
		}
		v.Spam = false
		v.Reasons = append(v.Reasons, "allowlisted sender")
	}
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
