package mtasts

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// negativeTTL is how long the absence of a policy is remembered, so a domain
// without MTA-STS is not re-probed on every message. It is deliberately short
// (well under a typical max_age) so a freshly published policy is picked up soon.
const negativeTTL = 5 * time.Minute

// fetchTimeout bounds the TXT lookup and the HTTPS policy GET together, so a slow
// or hostile policy host cannot stall a delivery.
const fetchTimeout = 10 * time.Second

// Resolver fetches and caches MTA-STS policies. The two lookups — the
// _mta-sts.<domain> TXT presence record and the HTTPS policy file — are injectable
// so the relay drives the real network while tests stay deterministic and offline.
type Resolver struct {
	// LookupTXT returns the TXT records of name; nil uses net.LookupTXT.
	LookupTXT func(name string) ([]string, error)
	// FetchPolicy GETs https://mta-sts.<domain>/.well-known/mta-sts.txt and returns
	// the body; nil uses fetchOverHTTPS, which requires a PKIX-valid certificate.
	FetchPolicy func(domain string) (string, error)
	// Now is the clock for cache expiry; nil uses time.Now.
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	policy  *Policy // nil records the proven absence of a policy
	expires time.Time
}

// Lookup returns the recipient domain's MTA-STS policy, or (nil, nil) when the
// domain publishes none. A found policy is cached for its max_age and an absence
// for negativeTTL; a network or parse failure is returned without caching, so the
// caller treats it as a transient condition rather than a withdrawn policy.
func (r *Resolver) Lookup(domain string) (*Policy, error) {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if e, ok := r.cached(domain, now()); ok {
		return e.policy, nil
	}

	// The TXT record is the cheap presence signal: no STSv1 record means the
	// domain has no policy, so the HTTPS fetch is skipped.
	present, err := r.txtPresent(domain)
	if err != nil {
		return nil, err
	}
	if !present {
		r.store(domain, nil, now().Add(negativeTTL))
		return nil, nil
	}

	fetch := r.FetchPolicy
	if fetch == nil {
		fetch = fetchOverHTTPS
	}
	body, err := fetch(domain)
	if err != nil {
		return nil, err
	}
	p, err := Parse(body)
	if err != nil {
		return nil, err
	}
	r.store(domain, p, now().Add(time.Duration(p.MaxAge)*time.Second))
	return p, nil
}

func (r *Resolver) cached(domain string, now time.Time) (cacheEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.cache[domain]
	if !ok || now.After(e.expires) {
		return cacheEntry{}, false
	}
	return e, true
}

func (r *Resolver) store(domain string, p *Policy, expires time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = make(map[string]cacheEntry)
	}
	r.cache[domain] = cacheEntry{policy: p, expires: expires}
}

// txtPresent reports whether _mta-sts.<domain> publishes an STSv1 record.
func (r *Resolver) txtPresent(domain string) (bool, error) {
	lookup := r.LookupTXT
	if lookup == nil {
		lookup = net.LookupTXT
	}
	records, err := lookup("_mta-sts." + domain)
	if err != nil {
		// A missing record is "no policy", not an error; only a real resolver
		// failure (SERVFAIL/timeout) propagates.
		if dnsErr, ok := err.(*net.DNSError); ok && dnsErr.IsNotFound {
			return false, nil
		}
		return false, err
	}
	for _, rec := range records {
		if strings.HasPrefix(strings.TrimSpace(rec), "v=STSv1") {
			return true, nil
		}
	}
	return false, nil
}

// fetchOverHTTPS GETs the well-known policy file. The URL is fixed to HTTPS and
// the default client validates the certificate, so the policy's authenticity
// rests on PKIX exactly as RFC 8461 §3.3 requires.
func fetchOverHTTPS(domain string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	url := "https://mta-sts." + domain + "/.well-known/mta-sts.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mtasts: policy fetch for %s: HTTP %d", domain, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	return string(body), nil
}
