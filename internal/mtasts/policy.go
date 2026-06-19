// Package mtasts implements SMTP MTA Strict Transport Security (RFC 8461): the
// relay uses it to decide when an outbound delivery must use validated TLS to a
// policy-matched MX rather than opportunistic STARTTLS that accepts any (or no)
// certificate. This file is the self-contained policy model: parsing an
// mta-sts.txt file and matching an MX host against it.
package mtasts

import (
	"fmt"
	"strconv"
	"strings"
)

// Mode is an MTA-STS policy mode (RFC 8461 §3.2).
type Mode string

const (
	ModeEnforce Mode = "enforce" // TLS to a policy-matched MX is mandatory
	ModeTesting Mode = "testing" // failures are surfaced but delivery still proceeds
	ModeNone    Mode = "none"    // the domain is withdrawing any prior policy
)

// Policy is a parsed MTA-STS policy file.
type Policy struct {
	Mode   Mode
	MX     []string // MX host patterns, lowercased, possibly with a leading "*."
	MaxAge int      // cache lifetime in seconds
}

// Parse reads an mta-sts.txt policy (RFC 8461 §3.2): CRLF-separated "key: value"
// lines. It requires version STSv1, a recognised mode, a positive max_age, and —
// unless the policy is withdrawing (mode none) — at least one mx pattern. Unknown
// keys are ignored, as the RFC requires for forward compatibility.
func Parse(text string) (*Policy, error) {
	var p Policy
	var version string
	for line := range strings.SplitSeq(text, "\n") {
		key, val, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "version":
			version = val
		case "mode":
			p.Mode = Mode(val)
		case "mx":
			if val != "" {
				p.MX = append(p.MX, strings.ToLower(val))
			}
		case "max_age":
			p.MaxAge, _ = strconv.Atoi(val)
		}
	}
	if version != "STSv1" {
		return nil, fmt.Errorf("mtasts: unsupported or missing version %q", version)
	}
	switch p.Mode {
	case ModeEnforce, ModeTesting, ModeNone:
	default:
		return nil, fmt.Errorf("mtasts: unknown mode %q", p.Mode)
	}
	if p.Mode != ModeNone && len(p.MX) == 0 {
		return nil, fmt.Errorf("mtasts: policy declares no mx patterns")
	}
	if p.MaxAge <= 0 {
		return nil, fmt.Errorf("mtasts: max_age must be a positive number of seconds")
	}
	return &p, nil
}

// MatchesMX reports whether host is permitted by the policy's mx patterns. A
// pattern "*.example.com" matches exactly one label in place of the star
// (foo.example.com, not foo.bar.example.com or example.com); any other pattern
// matches the host exactly. Matching is case-insensitive and ignores a trailing
// root dot (RFC 8461 §4.1).
func (p *Policy) MatchesMX(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pat := range p.MX {
		if matchMX(pat, host) {
			return true
		}
	}
	return false
}

func matchMX(pattern, host string) bool {
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		_, rest, ok := strings.Cut(host, ".")
		return ok && rest == suffix
	}
	return pattern == host
}
