package main

import (
	"encoding/xml"
	"net/http"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/serve"
)

// domainLister is the slice of the directory the autoconfig handler reads, to
// confirm a request names an active local domain.
type domainLister interface {
	ListDomains() ([]directory.DomainInfo, error)
}

// The two locations a mail client fetches its account configuration from
// (Thunderbird autoconfig, "clientConfig" version 1.1): the autoconfig.<domain>
// host and the well-known path on the mail domain itself.
const (
	autoconfigPath          = "/mail/config-v1.1.xml"
	autoconfigWellKnownPath = "/.well-known/autoconfig/mail/config-v1.1.xml"
)

type clientConfig struct {
	XMLName  xml.Name      `xml:"clientConfig"`
	Version  string        `xml:"version,attr"`
	Provider emailProvider `xml:"emailProvider"`
}

type emailProvider struct {
	ID          string         `xml:"id,attr"`
	Domain      string         `xml:"domain"`
	DisplayName string         `xml:"displayName"`
	ShortName   string         `xml:"displayShortName"`
	Incoming    []mailServerAd `xml:"incomingServer"`
	Outgoing    []mailServerAd `xml:"outgoingServer"`
}

type mailServerAd struct {
	Type       string `xml:"type,attr"`
	Hostname   string `xml:"hostname"`
	Port       int    `xml:"port"`
	SocketType string `xml:"socketType"`
	Username   string `xml:"username"`
	Auth       string `xml:"authentication"`
}

// withAutoconfig answers the mail-client autoconfiguration request locally and
// passes every other request to next. It answers only for an active local
// domain, so the server never advertises itself for a domain it does not host.
func withAutoconfig(hostname string, dir domainLister, logger *logging.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != autoconfigPath && r.URL.Path != autoconfigWellKnownPath {
			next.ServeHTTP(w, r)
			return
		}
		domain := autoconfigDomain(r)
		if domain == "" {
			http.Error(w, "no configuration", http.StatusNotFound)
			return
		}
		hosted, err := isActiveDomain(dir, domain)
		if err != nil {
			logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.Gateway,
				Name: "autoconfig.failed", RemoteAddr: serve.ClientAddr(r), Err: err.Error()})
			http.Error(w, "autoconfig unavailable", http.StatusInternalServerError)
			return
		}
		if !hosted {
			http.Error(w, "no configuration", http.StatusNotFound)
			return
		}
		writeClientConfig(w, hostname, domain)
	})
}

// autoconfigDomain picks the domain the client asks about: the part after the
// last '@' of the emailaddress parameter, else the Host minus a leading
// "autoconfig." label. The result is lower case; "" means none could be found.
func autoconfigDomain(r *http.Request) string {
	if addr := r.URL.Query().Get("emailaddress"); addr != "" {
		if i := strings.LastIndexByte(addr, '@'); i >= 0 {
			return strings.ToLower(addr[i+1:])
		}
		return ""
	}
	host := normalizeHost(r.Host)
	return strings.TrimPrefix(host, "autoconfig.")
}

func isActiveDomain(dir domainLister, domain string) (bool, error) {
	domains, err := dir.ListDomains()
	if err != nil {
		return false, err
	}
	for _, d := range domains {
		if d.Status == 0 && strings.EqualFold(d.Name, domain) {
			return true, nil
		}
	}
	return false, nil
}

// writeClientConfig writes the account settings: IMAP and POP3 over implicit
// TLS, and submission on 465 (implicit TLS) and 587 (STARTTLS), all signing in
// with the full address. Only TLS endpoints are advertised.
func writeClientConfig(w http.ResponseWriter, hostname, domain string) {
	server := func(typ string, port int, socket string) mailServerAd {
		return mailServerAd{Type: typ, Hostname: hostname, Port: port, SocketType: socket,
			Username: "%EMAILADDRESS%", Auth: "password-cleartext"}
	}
	cfg := clientConfig{Version: "1.1", Provider: emailProvider{
		ID: domain, Domain: domain, DisplayName: domain, ShortName: domain,
		Incoming: []mailServerAd{server("imap", 993, "SSL"), server("pop3", 995, "SSL")},
		Outgoing: []mailServerAd{server("smtp", 465, "SSL"), server("smtp", 587, "STARTTLS")},
	}}
	out, err := xml.MarshalIndent(cfg, "", "  ")
	if err != nil {
		http.Error(w, "autoconfig unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}
