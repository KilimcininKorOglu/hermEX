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
// what was found. It is a read-only diagnostic over the supplied resolver, it
// reports the live records rather than comparing against an expected target, so
// every result reflects real DNS state. selector is the domain's stored DKIM selector,
// so the check queries the name the domain's own key publishes under.
func checkDomainDNS(ctx context.Context, r dnsResolver, domain, hostname, selector string) dnsReport {
	c := &dnsChecker{ctx: ctx, r: r, domain: domain, selector: selector, rep: dnsReport{Domain: domain}}
	c.checkReachability(hostname)
	c.checkMX()
	c.checkAuth()
	c.checkAutodiscovery()
	c.checkServices()
	return c.rep
}

// dnsChecker accumulates a domain's health report one record class at a time.
type dnsChecker struct {
	ctx      context.Context
	r        dnsResolver
	domain   string
	selector string
	rep      dnsReport
}

// add records one result.
func (c *dnsChecker) add(label string, ok bool, detail string) {
	c.rep.Items = append(c.rep.Items, dnsCheckItem{Label: label, OK: ok, Detail: detail})
}

// host reports the addresses a name resolves to, or that it does not resolve.
func (c *dnsChecker) host(label, name string) {
	hosts, err := c.r.LookupHost(c.ctx, name)
	if err != nil || len(hosts) == 0 {
		c.add(label, false, name+" does not resolve")
		return
	}
	c.add(label, true, name+" → "+strings.Join(hosts, ", "))
}

// txt reports the first TXT record at name carrying the given prefix.
func (c *dnsChecker) txt(label, name, prefix, missing string) {
	recs, _ := c.r.LookupTXT(c.ctx, name)
	if v := findTXT(recs, prefix); v != "" {
		c.add(label, true, v)
		return
	}
	c.add(label, false, missing)
}

// srv reports the first SRV target for a service under the domain.
func (c *dnsChecker) srv(label, service, detailPrefix, missing string) {
	_, srv, err := c.r.LookupSRV(c.ctx, service, "tcp", c.domain)
	if err != nil || len(srv) == 0 {
		c.add(label, false, missing)
		return
	}
	c.add(label, true, detailPrefix+srvTarget(srv[0]))
}

// checkReachability verifies the server's mail host resolves publicly, or none of
// the per-domain records lead anywhere: every prescribed MX/CNAME/SRV target points
// at this host. hostname is the server's mail FQDN.
func (c *dnsChecker) checkReachability(hostname string) {
	if hostname == "" {
		return
	}
	c.host("Reachability", hostname)
}

// checkMX reports the domain's mail exchangers.
func (c *dnsChecker) checkMX() {
	mx, err := c.r.LookupMX(c.ctx, c.domain)
	if err != nil || len(mx) == 0 {
		c.add("MX", false, "no MX record")
		return
	}
	hosts := make([]string, len(mx))
	for i, m := range mx {
		hosts[i] = strings.TrimSuffix(m.Host, ".")
	}
	c.add("MX", true, strings.Join(hosts, ", "))
}

// checkAuth reports the SPF/DKIM/DMARC triad. The DKIM signing key is published as
// a TXT record at the domain's own selector, which prescribeDomainDNS instructs the
// owner to create, so the health check verifies the same record it prescribes.
func (c *dnsChecker) checkAuth() {
	c.txt("SPF", c.domain, "v=spf1", "no v=spf1 TXT record")
	selector := c.selector
	if selector == "" {
		selector = dkimSelector
	}
	dkimName := selector + "._domainkey." + c.domain
	c.txt("DKIM", dkimName, "v=DKIM1", "no v=DKIM1 TXT record at "+dkimName)
	c.txt("DMARC", "_dmarc."+c.domain, "v=DMARC1", "no _dmarc TXT record")
}

// checkAutodiscovery reports the Outlook/Thunderbird client-discovery records.
func (c *dnsChecker) checkAutodiscovery() {
	c.host("Autodiscover", "autodiscover."+c.domain)
	c.srv("Autodiscover SRV", "autodiscover", "", "no _autodiscover._tcp SRV record")
	c.host("Autoconfig", "autoconfig."+c.domain)
}

// checkServices reports the client-autoconfiguration service records: RFC 6186
// (IMAP/POP3/submission) and RFC 6764 (CalDAV/CardDAV) let clients discover the
// server by SRV lookup, and the DAV TXT advertises its well-known path.
// prescribeDomainDNS publishes these, so the check verifies the same records it
// prescribes; they are optional conveniences, so a missing one is reported, not
// treated as a failure.
func (c *dnsChecker) checkServices() {
	c.srvPair("CalDAV SRV", "caldavs", "caldav")
	c.srvPair("CardDAV SRV", "carddavs", "carddav")
	c.srvPair("IMAP SRV", "imaps", "imap")
	c.srvPair("POP3 SRV", "pop3s", "pop3")
	c.srv("Submission SRV", "submission", "_submission._tcp → ", "no _submission._tcp SRV record")
	c.checkDAVText()
}

// srvPair reports a protocol's implicit-TLS and plaintext SRV records together.
func (c *dnsChecker) srvPair(label, secure, plain string) {
	var found []string
	for _, service := range [...]string{secure, plain} {
		if _, srv, err := c.r.LookupSRV(c.ctx, service, "tcp", c.domain); err == nil && len(srv) > 0 {
			found = append(found, "_"+service+"._tcp → "+srvTarget(srv[0]))
		}
	}
	if len(found) == 0 {
		c.add(label, false, "no _"+secure+"/_"+plain+"._tcp SRV record")
		return
	}
	c.add(label, true, strings.Join(found, ", "))
}

// checkDAVText reports the TXT records advertising the DAV well-known path.
func (c *dnsChecker) checkDAVText() {
	var found []string
	for _, host := range []string{"_caldavs._tcp." + c.domain, "_carddavs._tcp." + c.domain} {
		recs, _ := c.r.LookupTXT(c.ctx, host)
		if p := findTXT(recs, "path="); p != "" {
			found = append(found, host+" → "+p)
		}
	}
	if len(found) == 0 {
		c.add("DAV TXT", false, `no _caldavs/_carddavs._tcp TXT "path=/dav" record`)
		return
	}
	c.add("DAV TXT", true, strings.Join(found, ", "))
}

// srvTarget renders an SRV record as "host:port".
func srvTarget(s *net.SRV) string {
	return strings.TrimSuffix(s.Target, ".") + ":" + strconv.Itoa(int(s.Port))
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
	writeJSON(w, checkDomainDNS(ctx, s.resolver, name, s.paths.ServerHostname(), s.dkimSelectorOf(name)))
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
	s.render(w, "dns-report", checkDomainDNS(ctx, s.resolver, name, s.paths.ServerHostname(), s.dkimSelectorOf(name)))
}
