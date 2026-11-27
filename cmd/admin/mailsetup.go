package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"hermex/internal/admin"
	"hermex/internal/directory"
	"hermex/internal/dkimsign"
)

// mailSetupDir is the part of the directory the provisioning commands below use,
// so each one is tested against a fake.
type mailSetupDir interface {
	DomainID(domain string) (int64, bool, error)
	GetDKIMKeyInfo(domain string) (directory.DKIMKeyInfo, bool, error)
	SetDKIMKey(domain, selector string, privPEM []byte, publicTXT string) error
	SetDKIMEnabled(domain string, enabled bool) error
	SetDomainAVScan(domain string, inbound, outbound bool) error
	GetMTASTSSettings() (directory.MTASTSSettings, bool, error)
	SetMTASTSSettings(s directory.MTASTSSettings) error
	SetTLSSettings(s directory.TLSSettings) error
}

// setMTASTS publishes the server's MTA-STS policy in the given mode (RFC 8461
// section 5: testing, enforce or none), or stops publishing it with "off", which
// keeps the stored mode. The rules are the panel's: a max age is whole seconds
// within RFC 8461's range, empty for the default, and enforce, which makes
// senders refuse delivery when TLS fails, needs its explicit confirmation.
func setMTASTS(d mailSetupDir, mode, maxAge string, confirmEnforce bool) error {
	st, _, err := d.GetMTASTSSettings()
	if err != nil {
		return err
	}
	switch mode {
	case "off":
		st.Enabled = false
	case "testing", "none", "enforce":
		st.Enabled, st.Mode = true, mode
	default:
		return fmt.Errorf("set-mtasts: mode must be off, testing, none or enforce, got %q", mode)
	}
	if st.MaxAge, err = mtastsMaxAge(maxAge, st.MaxAge); err != nil {
		return err
	}
	if st.Enabled && st.Mode == "enforce" && !confirmEnforce {
		return errors.New("set-mtasts: enforce makes senders refuse mail when TLS fails; pass --confirm-enforce")
	}
	return d.SetMTASTSSettings(st)
}

// mtastsMaxAge reads a max age in whole seconds within RFC 8461's range, keeping
// current when none is given.
func mtastsMaxAge(text string, current int) (int, error) {
	if text == "" {
		return current, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 || n > directory.MTASTSMaxMaxAge {
		return 0, fmt.Errorf("set-mtasts: max age %q is not 1..%d seconds", text, directory.MTASTSMaxMaxAge)
	}
	return n, nil
}

func runSetMTASTS(c *cmdContext) {
	maxAge, confirm := "", false
	for _, a := range c.args[2:] {
		if a == "--confirm-enforce" {
			confirm = true
		} else {
			maxAge = a
		}
	}
	if err := setMTASTS(c.dir, c.args[1], maxAge, confirm); err != nil {
		log.Fatalf("hermex-admin: %v", err)
	}
	outf("MTA-STS publishing set to %s; publish the records dns-records prints\n", c.args[1])
}

var errUnknownDomain = errors.New("no such domain")

func requireDomain(d mailSetupDir, domain string) error {
	_, ok, err := d.DomainID(domain)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: %w", domain, errUnknownDomain)
	}
	return nil
}

// generateDKIM gives the domain a signing key, stored disabled as the panel does:
// it signs nothing until its record is published and enable-dkim runs. A domain
// that already has a key keeps it, because replacing a published key breaks
// signing until the new record propagates, so an installer can run this on every
// install.
func generateDKIM(d mailSetupDir, domain string, generate func(string) ([]byte, string, error)) (string, error) {
	if err := requireDomain(d, domain); err != nil {
		return "", err
	}
	info, found, err := d.GetDKIMKeyInfo(domain)
	if err != nil {
		return "", err
	}
	if found {
		return fmt.Sprintf("domain %s already has a DKIM key (selector %s), left unchanged", domain, info.Selector), nil
	}
	privPEM, txt, err := generate(dkimsign.KeyRSA)
	if err != nil {
		return "", err
	}
	if err := d.SetDKIMKey(domain, admin.DefaultDKIMSelector, privPEM, txt); err != nil {
		return "", err
	}
	return fmt.Sprintf("DKIM key generated for %s (selector %s), not yet signing: publish its record, then run enable-dkim", domain, admin.DefaultDKIMSelector), nil
}

func enableDKIM(d mailSetupDir, domain string) error {
	_, found, err := d.GetDKIMKeyInfo(domain)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s has no DKIM key; run generate-dkim first", domain)
	}
	return d.SetDKIMEnabled(domain, true)
}

func setAVScan(d mailSetupDir, domain, state string) error {
	if err := requireDomain(d, domain); err != nil {
		return err
	}
	var on bool
	switch state {
	case "on":
		on = true
	case "off":
	default:
		return fmt.Errorf("set-av-scan: state must be on or off, got %q", state)
	}
	return d.SetDomainAVScan(domain, on, on)
}

// setTLSACME switches the front door to ACME certificates. The CA's terms must be
// accepted explicitly, as the panel requires, and the gateway reads the mode at
// startup, so it takes effect on the next gateway start.
func setTLSACME(d mailSetupDir, email string, agreed bool) error {
	if !agreed {
		return errors.New("set-tls-acme: pass --agree-tos to accept the certificate authority's terms of service")
	}
	if strings.Count(email, "@") != 1 || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		return fmt.Errorf("set-tls-acme: %q is not an email address", email)
	}
	return d.SetTLSSettings(directory.TLSSettings{Mode: "acme", ACMEEmail: email, ACMEAgreed: true})
}

// writeDNSRecords prints the records the domain must publish, one per line as
// type, name and value separated by tabs, the same set the panel prescribes.
func writeDNSRecords(w io.Writer, d mailSetupDir, domain, hostname string) error {
	if err := requireDomain(d, domain); err != nil {
		return err
	}
	info, found, err := d.GetDKIMKeyInfo(domain)
	if err != nil {
		return err
	}
	sts, _, err := d.GetMTASTSSettings()
	if err != nil {
		return err
	}
	name, value := "", ""
	if found {
		name, value = info.Selector+"._domainkey."+domain, info.PublicTXT
	}
	for _, r := range admin.PrescribeDomainDNS(domain, hostname, name, value, sts) {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", r.Type, r.Name, r.Value); err != nil {
			return err
		}
	}
	return nil
}

func runGenerateDKIM(c *cmdContext) {
	msg, err := generateDKIM(c.dir, c.args[1], dkimsign.GenerateKey)
	if err != nil {
		log.Fatalf("hermex-admin: generate-dkim: %v", err)
	}
	outln(msg)
}

func runEnableDKIM(c *cmdContext) {
	if err := enableDKIM(c.dir, c.args[1]); err != nil {
		log.Fatalf("hermex-admin: enable-dkim: %v", err)
	}
	outf("DKIM signing enabled for %s\n", c.args[1])
}

func runSetAVScan(c *cmdContext) {
	if err := setAVScan(c.dir, c.args[1], c.args[2]); err != nil {
		log.Fatalf("hermex-admin: %v", err)
	}
	outf("antivirus scanning %s for %s\n", c.args[2], c.args[1])
}

func runSetTLSACME(c *cmdContext) {
	agreed := c.args[2] == "--agree-tos"
	if err := setTLSACME(c.dir, c.args[1], agreed); err != nil {
		log.Fatalf("hermex-admin: %v", err)
	}
	outln("TLS mode set to acme; restart the gateway to obtain certificates")
}

func runDNSRecords(c *cmdContext) {
	if err := writeDNSRecords(os.Stdout, c.dir, c.args[1], c.cfg.Hostname); err != nil {
		log.Fatalf("hermex-admin: dns-records: %v", err)
	}
}
