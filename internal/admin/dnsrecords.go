package admin

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
// requirement, so the prescription stays complete.
func prescribeDomainDNS(domain, hostname, dkimName, dkimValue string) []prescribedRecord {
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
	return append(recs,
		prescribedRecord{Label: "DMARC", Name: "_dmarc." + domain, Type: "TXT",
			Value: "v=DMARC1; p=quarantine; rua=mailto:postmaster@" + domain,
			Note:  "Tells receivers how to handle mail that fails SPF and DKIM; tune the policy as you gain confidence."},
		prescribedRecord{Label: "Autodiscover", Name: "autodiscover." + domain, Type: "CNAME", Value: hostname,
			Note: "Points Outlook autodiscovery at the server."},
		prescribedRecord{Label: "Autoconfig", Name: "autoconfig." + domain, Type: "CNAME", Value: hostname,
			Note: "Points Thunderbird automatic configuration at the server."},
		prescribedRecord{Label: "Autodiscover SRV", Name: "_autodiscover._tcp." + domain, Type: "SRV",
			Value: "0 0 443 " + hostname,
			Note:  "SRV fallback for clients that look up autodiscovery by service record."},
	)
}
