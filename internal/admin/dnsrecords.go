package admin

import (
	"hermex/internal/directory"
	"hermex/internal/mtasts"
)

// prescribedRecord is one DNS record a domain owner must publish for mail to
// route and authenticate through this server. Unlike dnsCheckItem (dnscheck.go),
// which reports what a domain *currently* resolves to, this prescribes the target
// state the owner should configure: the host to create the record at, its type,
// the value to publish, and a short note on what it enables.
type prescribedRecord struct {
	Label string // record class, e.g. "MX", "SPF", "DKIM"
	Name  string // fully-qualified host to create the record at
	Type  string // DNS record type: MX, TXT, CNAME, SRV
	Value string // value to publish
	Note  string // what this record enables
}

// prescribeDomainDNS returns the full set of DNS records a domain owner must
// publish so mail for domain routes to this server and passes SPF/DKIM/DMARC.
// hostname is the server's public mail FQDN — the MX target and the
// autodiscover/autoconfig host clients are pointed at. dkimName/dkimValue carry
// the domain's generated DKIM record; both empty means no key exists yet, and the
// DKIM row points the owner at the DKIM panel rather than dropping the
// requirement, so the prescription stays complete. When MTA-STS publishing is
// enabled (sts.Enabled) the prescription also includes the policy host, the
// _mta-sts presence record carrying the current policy id, and the TLSRPT reporting
// record; these are omitted when publishing is off, since their host serves no
// policy until then.
func prescribeDomainDNS(domain, hostname, dkimName, dkimValue string, sts directory.MTASTSSettings) []prescribedRecord {
	recs := []prescribedRecord{
		{Label: "MX", Name: domain, Type: "MX", Value: "10 " + hostname,
			Note: "Routes inbound mail for this domain to the server."},
		{Label: "SPF", Name: domain, Type: "TXT", Value: "v=spf1 mx ~all",
			Note: "Authorizes the server (this domain's MX host) to send mail for it."},
	}
	// The DKIM value is whatever the domain's signing key produced; without a key
	// there is nothing valid to publish yet, so the row carries a generate-first
	// note instead of a placeholder that could be mistaken for a real record.
	if dkimValue != "" {
		recs = append(recs, prescribedRecord{Label: "DKIM", Name: dkimName, Type: "TXT", Value: dkimValue,
			Note: "Lets receivers verify the signature on this domain's outbound mail."})
	} else {
		recs = append(recs, prescribedRecord{Label: "DKIM", Name: dkimSelector + "._domainkey." + domain, Type: "TXT",
			Value: "generate a DKIM key in the DKIM panel above, then publish the record it shows",
			Note:  "Lets receivers verify the signature on this domain's outbound mail."})
	}
	recs = append(recs,
		prescribedRecord{Label: "DMARC", Name: "_dmarc." + domain, Type: "TXT",
			Value: "v=DMARC1; p=quarantine; rua=mailto:postmaster@" + domain,
			Note:  "Tells receivers how to handle mail that fails SPF and DKIM; tune the policy as you gain confidence."},
		prescribedRecord{Label: "Mail host", Name: "mail." + domain, Type: "CNAME", Value: hostname,
			Note: "Points mail." + domain + " at the server for IMAP/POP3/SMTP clients, and lets the server obtain a TLS certificate for this host automatically in ACME mode."},
		prescribedRecord{Label: "Autodiscover", Name: "autodiscover." + domain, Type: "CNAME", Value: hostname,
			Note: "Points Outlook autodiscovery at the server, and lets the server obtain a TLS certificate for this host automatically in ACME mode."},
		prescribedRecord{Label: "Autoconfig", Name: "autoconfig." + domain, Type: "CNAME", Value: hostname,
			Note: "Points Thunderbird automatic configuration at the server, and lets the server obtain a TLS certificate for this host automatically in ACME mode."},
		prescribedRecord{Label: "Autodiscover SRV", Name: "_autodiscover._tcp." + domain, Type: "SRV",
			Value: "0 0 443 " + hostname,
			Note:  "SRV fallback for clients that look up autodiscovery by service record."},
	)
	// Client-autoconfiguration service records let IMAP/POP3/SMTP (RFC 6186) and
	// CalDAV/CardDAV (RFC 6764) clients find the server by SRV lookup without manual
	// host entry; the DAV TXT advertises the well-known path. These are optional
	// conveniences (mail still flows without them), and the health check verifies the
	// same records. Only the TLS variants are prescribed — clients should not connect
	// in the clear — so the secure SRV names carry the standard implicit-TLS ports.
	recs = append(recs,
		prescribedRecord{Label: "IMAP SRV", Name: "_imaps._tcp." + domain, Type: "SRV", Value: "0 0 993 " + hostname,
			Note: "Lets mail clients discover the IMAP server for this domain."},
		prescribedRecord{Label: "POP3 SRV", Name: "_pop3s._tcp." + domain, Type: "SRV", Value: "0 0 995 " + hostname,
			Note: "Lets mail clients discover the POP3 server for this domain."},
		prescribedRecord{Label: "Submission SRV", Name: "_submission._tcp." + domain, Type: "SRV", Value: "0 0 587 " + hostname,
			Note: "Lets mail clients discover the SMTP submission server for this domain."},
		prescribedRecord{Label: "CalDAV SRV", Name: "_caldavs._tcp." + domain, Type: "SRV", Value: "0 0 443 " + hostname,
			Note: "Lets CalDAV clients discover the calendar server for this domain."},
		prescribedRecord{Label: "CardDAV SRV", Name: "_carddavs._tcp." + domain, Type: "SRV", Value: "0 0 443 " + hostname,
			Note: "Lets CardDAV clients discover the contacts server for this domain."},
		prescribedRecord{Label: "DAV TXT", Name: "_caldavs._tcp." + domain, Type: "TXT", Value: "path=/dav",
			Note: "Advertises the DAV well-known path for autodiscovery; publish the same TXT at _carddavs._tcp." + domain + "."},
	)
	if sts.Enabled {
		// The policy id is derived from the served policy bytes (mode + max_age + this
		// MX), so the published _mta-sts id matches exactly what the gateway serves and
		// changes whenever the policy does.
		id := mtasts.PolicyID(mtasts.Build(mtasts.Policy{Mode: mtasts.Mode(sts.Mode), MX: []string{hostname}, MaxAge: sts.MaxAge}))
		recs = append(recs,
			prescribedRecord{Label: "MTA-STS host", Name: "mta-sts." + domain, Type: "CNAME", Value: hostname,
				Note: "Points mta-sts." + domain + " at the server, which publishes the MTA-STS policy over HTTPS; lets the server obtain a TLS certificate for this host automatically in ACME mode."},
			prescribedRecord{Label: "MTA-STS", Name: "_mta-sts." + domain, Type: "TXT", Value: "v=STSv1; id=" + id,
				Note: "Signals that this domain publishes an MTA-STS policy; senders re-fetch when the id changes, so republish this record after changing the policy mode or max age."},
			prescribedRecord{Label: "TLS reporting", Name: "_smtp._tls." + domain, Type: "TXT",
				Value: "v=TLSRPTv1; rua=mailto:postmaster@" + domain,
				Note:  "Asks senders to report TLS problems delivering to this domain (RFC 8460); point rua at any mailbox you watch."},
		)
	}
	return recs
}
