package admin

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// dnsResolver is the lookup surface the DNS health check needs; *net.Resolver
// satisfies it, and tests inject a scripted resolver. Each method takes a context
// so the check can be bounded by a timeout.
type dnsResolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
}

// dnsCheckItem is one resolved record class: whether it was found and a short
// human detail (the resolved value, or why it is missing).
type dnsCheckItem struct {
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// dnsReport is the result of a domain's DNS health check.
type dnsReport struct {
	Domain string         `json:"domain"`
	Items  []dnsCheckItem `json:"items"`
}

// checkDomainDNS resolves the mail-relevant DNS records for a domain and reports
// what was found. It is a read-only diagnostic over the supplied resolver — it
// reports the live records rather than comparing against an expected target, so
// every result reflects real DNS state.
func checkDomainDNS(ctx context.Context, r dnsResolver, domain, hostname string) dnsReport {
	rep := dnsReport{Domain: domain}
	add := func(label string, ok bool, detail string) {
		rep.Items = append(rep.Items, dnsCheckItem{Label: label, OK: ok, Detail: detail})
	}

	// Reachability: the server's mail host must resolve publicly, or none of the
	// per-domain records below lead anywhere — every prescribed MX/CNAME/SRV target
	// points at this host. hostname is the server's mail FQDN.
	if hostname != "" {
		if hosts, err := r.LookupHost(ctx, hostname); err == nil && len(hosts) > 0 {
			add("Reachability", true, hostname+" → "+strings.Join(hosts, ", "))
		} else {
			add("Reachability", false, hostname+" does not resolve")
		}
	}

	if mx, err := r.LookupMX(ctx, domain); err == nil && len(mx) > 0 {
		hosts := make([]string, len(mx))
		for i, m := range mx {
			hosts[i] = strings.TrimSuffix(m.Host, ".")
		}
		add("MX", true, strings.Join(hosts, ", "))
	} else {
		add("MX", false, "no MX record")
	}

	txt, _ := r.LookupTXT(ctx, domain)
	if spf := findTXT(txt, "v=spf1"); spf != "" {
		add("SPF", true, spf)
	} else {
		add("SPF", false, "no v=spf1 TXT record")
	}

	// DKIM completes the SPF/DKIM/DMARC auth triad: the signing key is published as
	// a TXT record at the server's selector, which prescribeDomainDNS instructs the
	// owner to create, so the health check verifies the same record it prescribes.
	dkimName := dkimSelector + "._domainkey." + domain
	dkimTXT, _ := r.LookupTXT(ctx, dkimName)
	if dkim := findTXT(dkimTXT, "v=DKIM1"); dkim != "" {
		add("DKIM", true, dkim)
	} else {
		add("DKIM", false, "no v=DKIM1 TXT record at "+dkimName)
	}

	dmarcTXT, _ := r.LookupTXT(ctx, "_dmarc."+domain)
	if dmarc := findTXT(dmarcTXT, "v=DMARC1"); dmarc != "" {
		add("DMARC", true, dmarc)
	} else {
		add("DMARC", false, "no _dmarc TXT record")
	}

	if hosts, err := r.LookupHost(ctx, "autodiscover."+domain); err == nil && len(hosts) > 0 {
		add("Autodiscover", true, "autodiscover."+domain+" → "+strings.Join(hosts, ", "))
	} else {
		add("Autodiscover", false, "autodiscover."+domain+" does not resolve")
	}

	if _, srv, err := r.LookupSRV(ctx, "autodiscover", "tcp", domain); err == nil && len(srv) > 0 {
		add("Autodiscover SRV", true, strings.TrimSuffix(srv[0].Target, ".")+":"+strconv.Itoa(int(srv[0].Port)))
	} else {
		add("Autodiscover SRV", false, "no _autodiscover._tcp SRV record")
	}

	if hosts, err := r.LookupHost(ctx, "autoconfig."+domain); err == nil && len(hosts) > 0 {
		add("Autoconfig", true, "autoconfig."+domain+" → "+strings.Join(hosts, ", "))
	} else {
		add("Autoconfig", false, "autoconfig."+domain+" does not resolve")
	}

	// Client-autoconfiguration service records: RFC 6186 (IMAP/POP3/submission) and
	// RFC 6764 (CalDAV/CardDAV) let clients discover the server by SRV lookup, and
	// the DAV TXT advertises its well-known path. prescribeDomainDNS publishes these,
	// so the check verifies the same records it prescribes; they are optional
	// conveniences, so a missing one is reported, not treated as a failure.
	srvTarget := func(s *net.SRV) string {
		return strings.TrimSuffix(s.Target, ".") + ":" + strconv.Itoa(int(s.Port))
	}
	srvPair := func(label, secure, plain string) {
		var found []string
		if _, srv, err := r.LookupSRV(ctx, secure, "tcp", domain); err == nil && len(srv) > 0 {
			found = append(found, "_"+secure+"._tcp → "+srvTarget(srv[0]))
		}
		if _, srv, err := r.LookupSRV(ctx, plain, "tcp", domain); err == nil && len(srv) > 0 {
			found = append(found, "_"+plain+"._tcp → "+srvTarget(srv[0]))
		}
		if len(found) > 0 {
			add(label, true, strings.Join(found, ", "))
		} else {
			add(label, false, "no _"+secure+"/_"+plain+"._tcp SRV record")
		}
	}
	srvPair("CalDAV SRV", "caldavs", "caldav")
	srvPair("CardDAV SRV", "carddavs", "carddav")
	srvPair("IMAP SRV", "imaps", "imap")
	srvPair("POP3 SRV", "pop3s", "pop3")

	if _, srv, err := r.LookupSRV(ctx, "submission", "tcp", domain); err == nil && len(srv) > 0 {
		add("Submission SRV", true, "_submission._tcp → "+srvTarget(srv[0]))
	} else {
		add("Submission SRV", false, "no _submission._tcp SRV record")
	}

	var davTXT []string
	for _, host := range []string{"_caldavs._tcp." + domain, "_carddavs._tcp." + domain} {
		if recs, _ := r.LookupTXT(ctx, host); findTXT(recs, "path=") != "" {
			davTXT = append(davTXT, host+" → "+findTXT(recs, "path="))
		}
	}
	if len(davTXT) > 0 {
		add("DAV TXT", true, strings.Join(davTXT, ", "))
	} else {
		add("DAV TXT", false, `no _caldavs/_carddavs._tcp TXT "path=/dav" record`)
	}

	return rep
}

// findTXT returns the first TXT record beginning with prefix (case-insensitively),
// or "" when none matches.
func findTXT(records []string, prefix string) string {
	for _, t := range records {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), strings.ToLower(prefix)) {
			return t
		}
	}
	return ""
}

// resolveDomainName maps the route's domain id to its name, writing the matching
// error response and returning ok=false when the id is bad or unknown.
func (s *Server) resolveDomainName(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return "", false
	}
	dd, found, err := s.dir.GetDomain(id)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return "", false
	}
	if !found {
		http.Error(w, "no such domain", http.StatusNotFound)
		return "", false
	}
	return dd.Name, true
}

// handleGetDomainDNS runs the DNS health check for a domain and returns the report
// as JSON (system administrators only).
func (s *Server) handleGetDomainDNS(w http.ResponseWriter, r *http.Request) {
	name, ok := s.resolveDomainName(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, checkDomainDNS(ctx, s.resolver, name, s.paths.ServerHostname()))
}

// handleUIDomainDNS runs the DNS health check and returns the report partial for
// htmx to swap into the domain detail page.
func (s *Server) handleUIDomainDNS(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	name, ok := s.resolveDomainName(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	s.render(w, "dns-report", checkDomainDNS(ctx, s.resolver, name, s.paths.ServerHostname()))
}
