package antispam

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-msgauth/dmarc"

	"hermex/internal/mime"
)

// Every domain these checks query is one the connecting client chose, so the
// nameserver that answers it is one the sender may control. They all run inline in
// the SMTP DATA phase, before the 250 is written, and the listener accepts
// connections without a concurrency cap. A sender whose nameserver simply never
// answers would otherwise hold a goroutine and socket for the OS resolver's full
// retry budget on every query, so each lookup carries a deadline and degrades to
// the fail-open answer it already gives for any other lookup failure. This mirrors
// the bounded PTR lookup behind the Received header.
//
// The values are variables so a test can shorten them; nothing else writes them.
var (
	// dnsTimeout bounds a single query, matching the PTR lookup's budget.
	dnsTimeout = 3 * time.Second
	// spfTimeout bounds an entire SPF evaluation, which RFC 7208 permits to chase
	// up to ten records. A legitimate chain resolves in well under a second.
	spfTimeout = 5 * time.Second
)

// resolver performs these checks' DNS queries. It is a variable so a test can
// point it at a nameserver that never answers and prove the deadlines hold.
var resolver = net.DefaultResolver

// realSPF evaluates SPF for the connecting client and maps the RFC 7208 result to
// an AuthResult. The library's error is advisory (it can be non-nil even on a
// successful check), so only the Result drives the verdict. A lookup that outruns
// the deadline surfaces as TempError, which maps to AuthError, the same advisory
// answer any other resolution failure gives.
func realSPF(ip net.IP, helo, mailFrom string) AuthResult {
	ctx, cancel := context.WithTimeout(context.Background(), spfTimeout)
	defer cancel()
	res, _ := spf.CheckHostWithSender(ip, helo, mailFrom,
		spf.WithContext(ctx), spf.WithResolver(resolver))
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
// record exists (or the lookup errors, including outrunning its deadline), which
// the scorer treats as no policy.
func realDMARC(domain string) (policy string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()
	// The library prefixes _dmarc. itself, so the callback resolves the name it is
	// handed; its only job here is to carry the deadline the bare net.LookupTXT
	// default cannot.
	rec, err := dmarc.LookupWithOptions(domain, &dmarc.LookupOptions{
		LookupTXT: func(name string) ([]string, error) { return resolver.LookupTXT(ctx, name) },
	})
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
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()
	// A DNSBL answers with an A record in 127/8, and isListed discards anything
	// else, so asking only for IPv4 drops a pointless AAAA query per zone without
	// changing the answer. Zones are checked in sequence, so a per-zone deadline is
	// what bounds the whole loop.
	addrs, err := resolver.LookupIP(ctx, "ip4", q)
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

// MessageText extracts the text the Bayes model classifies: the subject plus the
// decoded text/* body parts. Training (the bootstrap tool, self-training) and
// live scoring must both go through it so their token vocabularies match. It is
// best-effort — on a parse error it returns whatever was gathered (possibly just
// the subject).
func MessageText(raw []byte) string {
	var b strings.Builder
	if env, err := mime.ParseEnvelope(raw); err == nil {
		b.WriteString(env.Subject)
		b.WriteByte(' ')
	}
	collectText(mime.ParseStructure(raw), &b)
	return b.String()
}

// collectText appends the decoded text of every text/* part in the tree.
func collectText(p *mime.Part, b *strings.Builder) {
	if p == nil {
		return
	}
	if p.Type == "text" {
		if t, err := p.DecodedText(); err == nil {
			b.WriteString(t)
			b.WriteByte(' ')
		}
	}
	for _, c := range p.Children {
		collectText(c, b)
	}
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
		for _, v := range slices.Backward(v6) {
			fmt.Fprintf(&b, "%x.%x.", v&0x0f, v>>4)
		}
		b.WriteString(zone)
		return b.String()
	}
	return ""
}
