// Package dane implements opportunistic DANE TLS for outbound SMTP (RFC 7672):
// it looks up a mail exchanger's TLSA records through a DNSSEC-validating
// resolver and authenticates the server's certificate against them. When a
// destination publishes usable, DNSSEC-secure TLSA records, TLS becomes
// mandatory and authenticated; otherwise delivery falls back to the caller's
// pre-DANE opportunistic path.
//
// The trust model is the reference (Postfix) one: the operator points the
// resolver at a security-aware validating resolver reached over a trusted
// channel (typically loopback), and this package trusts that resolver's
// AuthenticatedData (AD) bit as the signal that a TLSA RRset is DNSSEC-secure.
// A record set without the AD bit is treated as insecure (DANE not applicable),
// and a SERVFAIL (bogus) response is a lookup failure that defers delivery
// rather than silently downgrading to cleartext.
package dane

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/miekg/dns"
)

// TLSA certificate usages, selectors, and matching types relevant to SMTP DANE
// (RFC 7672 §3.1, RFC 6698). Only DANE-EE(3) and DANE-TA(2) are usable for SMTP;
// PKIX-TA(0)/PKIX-EE(1) are treated as unusable. Matching type Full(0) is
// "discouraged" by RFC 7672 and not supported here, so only SHA-256(1) and
// SHA-512(2) digests are honored.
const (
	usageDANETA = 2
	usageDANEEE = 3

	selectorCert = 0 // match the full certificate DER
	selectorSPKI = 1 // match the SubjectPublicKeyInfo DER

	matchSHA256 = 1
	matchSHA512 = 2
)

// Record is one usable TLSA record: the certificate-association data plus the
// usage/selector/matching-type that say how to compare it to the server's chain.
type Record struct {
	Usage        uint8
	Selector     uint8
	MatchingType uint8
	Data         []byte
}

// usable reports whether a TLSA record is one this package can act on: a
// DANE-EE/DANE-TA usage, a cert-or-SPKI selector, and a SHA-256/512 matching
// type. RFC 7672 lets a client treat any other record as unusable.
func (r Record) usable() bool {
	if r.Usage != usageDANETA && r.Usage != usageDANEEE {
		return false
	}
	if r.Selector != selectorCert && r.Selector != selectorSPKI {
		return false
	}
	return r.MatchingType == matchSHA256 || r.MatchingType == matchSHA512
}

// Resolver looks up TLSA records through a single DNSSEC-validating resolver.
// Addr is the resolver's address; a bare host gets the default DNS port. It must
// be a security-aware validating resolver reached over a trusted channel, since
// this package trusts its AD bit.
type Resolver struct {
	Addr    string
	Timeout time.Duration // per-query timeout; <=0 uses a 5s default
}

func (r *Resolver) addr() string {
	if _, _, err := net.SplitHostPort(r.Addr); err != nil {
		return net.JoinHostPort(r.Addr, "53")
	}
	return r.Addr
}

func (r *Resolver) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 5 * time.Second
}

// LookupTLSA queries the TLSA RRset for an SMTP server reached at host:port
// (the query name is "_<port>._tcp.<host>"). It returns the usable records and
// whether DANE is applicable: true only when the RRset is DNSSEC-secure (the
// resolver set the AD bit) and at least one usable record was found. A SERVFAIL
// (bogus) response is an error so the caller defers rather than downgrades; an
// insecure or absent RRset returns (nil, false, nil) so the caller falls back to
// opportunistic TLS.
func (r *Resolver) LookupTLSA(host string, port int) ([]Record, bool, error) {
	name := fmt.Sprintf("_%d._tcp.%s", port, dns.Fqdn(host))
	resp, err := r.query(name)
	if err != nil {
		return nil, false, err
	}
	switch resp.Rcode {
	case dns.RcodeSuccess, dns.RcodeNameError: // NOERROR or NXDOMAIN
	case dns.RcodeServerFailure:
		return nil, false, fmt.Errorf("dane: SERVFAIL (bogus DNSSEC) resolving %s", name)
	default:
		return nil, false, fmt.Errorf("dane: TLSA query for %s returned rcode %d", name, resp.Rcode)
	}
	// Without the AD bit the answer is not DNSSEC-validated, so DANE does not
	// apply and the caller must fall back to opportunistic TLS rather than treat
	// an unauthenticated record as authoritative.
	if !resp.AuthenticatedData {
		return nil, false, nil
	}
	var usable []Record
	for _, rr := range resp.Answer {
		t, ok := rr.(*dns.TLSA)
		if !ok {
			continue
		}
		data, err := hex.DecodeString(t.Certificate)
		if err != nil {
			continue // a malformed association is treated as unusable, not fatal
		}
		rec := Record{Usage: t.Usage, Selector: t.Selector, MatchingType: t.MatchingType, Data: data}
		if rec.usable() {
			usable = append(usable, rec)
		}
	}
	return usable, len(usable) > 0, nil
}

// query sends one TLSA question with DNSSEC requested (EDNS0 DO bit), retrying
// over TCP when the UDP answer is truncated (TLSA RRsets with DNSSEC signatures
// often exceed the UDP payload).
func (r *Resolver) query(name string) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetQuestion(name, dns.TypeTLSA)
	m.SetEdns0(4096, true)
	m.RecursionDesired = true

	c := &dns.Client{Net: "udp", Timeout: r.timeout()}
	resp, _, err := c.Exchange(m, r.addr())
	if err != nil {
		return nil, err
	}
	if resp.Truncated {
		c.Net = "tcp"
		resp, _, err = c.Exchange(m, r.addr())
		if err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// Match authenticates a server's presented certificate chain against the TLSA
// records (RFC 7672 §3). It returns nil on the first record that matches under
// its usage's rules, and an error when none does (the caller MUST NOT deliver in
// that case). chain[0] is the leaf; host is the SMTP server hostname used for
// the DANE-TA(2) name check.
func Match(recs []Record, chain []*x509.Certificate, host string) error {
	if len(chain) == 0 {
		return errors.New("dane: server presented no certificate")
	}
	for _, rec := range recs {
		switch rec.Usage {
		case usageDANEEE:
			// DANE-EE(3): the record matches the leaf directly, with no PKIX
			// chain or name check (RFC 7672 §3.1.1, §3.2.1).
			if certMatches(rec, chain[0]) {
				return nil
			}
		case usageDANETA:
			// DANE-TA(2): a record matches a trust anchor in the presented
			// chain, the leaf must chain to it, and a name check binds the leaf
			// to host (RFC 7672 §3.1.2, §3.2.2).
			for _, ca := range chain {
				if certMatches(rec, ca) && chainsTo(chain, ca, host) {
					return nil
				}
			}
		}
	}
	return errors.New("dane: no TLSA record matched the server certificate")
}

// certMatches reports whether a TLSA record's association data equals the
// selected, digested bytes of cert.
func certMatches(rec Record, cert *x509.Certificate) bool {
	var selected []byte
	switch rec.Selector {
	case selectorCert:
		selected = cert.Raw
	case selectorSPKI:
		selected = cert.RawSubjectPublicKeyInfo
	default:
		return false
	}
	var digest []byte
	switch rec.MatchingType {
	case matchSHA256:
		sum := sha256.Sum256(selected)
		digest = sum[:]
	case matchSHA512:
		sum := sha512.Sum512(selected)
		digest = sum[:]
	default:
		return false
	}
	return bytes.Equal(digest, rec.Data)
}

// chainsTo verifies the leaf chains to the trust anchor ta using the other
// presented certificates as intermediates, and that the leaf is valid for host.
func chainsTo(chain []*x509.Certificate, ta *x509.Certificate, host string) bool {
	roots := x509.NewCertPool()
	roots.AddCert(ta)
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		if c.Equal(ta) {
			continue
		}
		inter.AddCert(c)
	}
	_, err := chain[0].Verify(x509.VerifyOptions{
		DNSName:       host,
		Roots:         roots,
		Intermediates: inter,
	})
	return err == nil
}
