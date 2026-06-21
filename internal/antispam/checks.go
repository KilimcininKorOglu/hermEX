package antispam

import (
	"bytes"
	"fmt"
	"net"
	"strings"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-msgauth/dmarc"
)

// realSPF evaluates SPF for the connecting client and maps the RFC 7208 result to
// an AuthResult. The library's error is advisory (it can be non-nil even on a
// successful check), so only the Result drives the verdict.
func realSPF(ip net.IP, helo, mailFrom string) AuthResult {
	res, _ := spf.CheckHostWithSender(ip, helo, mailFrom)
	switch res {
	case spf.Pass:
		return AuthPass
	case spf.Fail:
		return AuthFail
	case spf.SoftFail:
		return AuthSoftFail
	case spf.Neutral:
		return AuthNeutral
	case spf.None:
		return AuthNone
	default: // TempError, PermError
		return AuthError
	}
}

// realDKIM verifies the message's DKIM signatures and returns each signature's
// claiming domain with whether it validated. A parse error yields no results, so
// the scorer treats the message as unsigned.
func realDKIM(raw []byte) []DKIMResult {
	vs, err := dkim.Verify(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	out := make([]DKIMResult, 0, len(vs))
	for _, v := range vs {
		out = append(out, DKIMResult{Domain: v.Domain, Valid: v.Err == nil})
	}
	return out
}

// realDMARC fetches the From domain's published DMARC policy. ok is false when no
// record exists (or the lookup errors), which the scorer treats as no policy.
func realDMARC(domain string) (policy string, ok bool) {
	rec, err := dmarc.Lookup(domain)
	if err != nil || rec == nil {
		return "", false
	}
	return string(rec.Policy), true
}

// realDNSBL reports whether the client IP is listed on a DNS blocklist zone. A
// lookup error (including NXDOMAIN, the not-listed answer) reports not listed, so
// DNSBL is fail-open like every other check.
func realDNSBL(ip net.IP, zone string) bool {
	q := dnsblQuery(ip, zone)
	if q == "" {
		return false
	}
	addrs, err := net.LookupIP(q)
	if err != nil {
		return false
	}
	return isListed(addrs)
}

// isListed reports whether a DNSBL response signals a real listing: an answer in
// 127.0.0.0/8 (the RFC 5782 convention). Any other address — a hijacked or
// wildcard resolver returning a public A record — is rejected: a false positive
// would file a legitimate sender's mail to Junk, so the bar is the standard one.
func isListed(addrs []net.IP) bool {
	for _, a := range addrs {
		if a4 := a.To4(); a4 != nil && a4[0] == 127 {
			return true
		}
	}
	return false
}

// dnsblQuery builds the DNSBL lookup name for an IP and zone: the IP's octets
// (IPv4) or nibbles (IPv6) reversed and prefixed to the zone. It returns "" for
// an IP it cannot represent.
func dnsblQuery(ip net.IP, zone string) string {
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.%d.%s", v4[3], v4[2], v4[1], v4[0], zone)
	}
	if v6 := ip.To16(); v6 != nil {
		var b strings.Builder
		for i := len(v6) - 1; i >= 0; i-- {
			fmt.Fprintf(&b, "%x.%x.", v6[i]&0x0f, v6[i]>>4)
		}
		b.WriteString(zone)
		return b.String()
	}
	return ""
}
