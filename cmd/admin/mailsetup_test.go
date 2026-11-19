package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// fakeSetupDir records what the provisioning commands write.
type fakeSetupDir struct {
	domains   map[string]bool
	dkim      map[string]directory.DKIMKeyInfo
	keyWrites int
	av        map[string]bool
	tls       *directory.TLSSettings
}

func newFakeSetupDir(domains ...string) *fakeSetupDir {
	f := &fakeSetupDir{domains: map[string]bool{}, dkim: map[string]directory.DKIMKeyInfo{}, av: map[string]bool{}}
	for _, d := range domains {
		f.domains[d] = true
	}
	return f
}

func (f *fakeSetupDir) DomainID(d string) (int64, bool, error) { return 1, f.domains[d], nil }
func (f *fakeSetupDir) GetDKIMKeyInfo(d string) (directory.DKIMKeyInfo, bool, error) {
	info, ok := f.dkim[d]
	return info, ok, nil
}
func (f *fakeSetupDir) SetDKIMKey(d, sel string, _ []byte, txt string) error {
	f.keyWrites++
	f.dkim[d] = directory.DKIMKeyInfo{Selector: sel, PublicTXT: txt}
	return nil
}
func (f *fakeSetupDir) SetDKIMEnabled(d string, on bool) error {
	info := f.dkim[d]
	info.Enabled = on
	f.dkim[d] = info
	return nil
}
func (f *fakeSetupDir) SetDomainAVScan(d string, in, _ bool) error { f.av[d] = in; return nil }
func (f *fakeSetupDir) GetMTASTSSettings() (directory.MTASTSSettings, bool, error) {
	return directory.MTASTSSettings{}, false, nil
}
func (f *fakeSetupDir) SetTLSSettings(s directory.TLSSettings) error { f.tls = &s; return nil }

func fakeKey(string) ([]byte, string, error) { return []byte("key"), "v=DKIM1; k=rsa; p=AAAA", nil }

// TestGenerateDKIMKeepsAnExistingKey: an installer runs generate-dkim on every
// install, and replacing a published key would break signing, so a domain that
// has a key must keep it and a new one is stored disabled.
func TestGenerateDKIMKeepsAnExistingKey(t *testing.T) {
	d := newFakeSetupDir("example.com")
	if _, err := generateDKIM(d, "example.com", fakeKey); err != nil {
		t.Fatal(err)
	}
	if d.keyWrites != 1 || d.dkim["example.com"].Enabled {
		t.Fatalf("first run: writes=%d enabled=%v, want one disabled key", d.keyWrites, d.dkim["example.com"].Enabled)
	}
	if _, err := generateDKIM(d, "example.com", fakeKey); err != nil {
		t.Fatal(err)
	}
	if d.keyWrites != 1 {
		t.Errorf("second run wrote a key again (%d writes); an existing key must be left unchanged", d.keyWrites)
	}
}

// TestSetupCommandsRefuseAnUnknownDomain: a mistyped domain must fail instead of
// writing a key or a scan setting for a domain that does not exist.
func TestSetupCommandsRefuseAnUnknownDomain(t *testing.T) {
	d := newFakeSetupDir("example.com")
	if _, err := generateDKIM(d, "typo.example", fakeKey); !errors.Is(err, errUnknownDomain) {
		t.Errorf("generate-dkim on an unknown domain: err %v, want errUnknownDomain", err)
	}
	if err := setAVScan(d, "typo.example", "on"); !errors.Is(err, errUnknownDomain) {
		t.Errorf("set-av-scan on an unknown domain: err %v, want errUnknownDomain", err)
	}
	if err := writeDNSRecords(&bytes.Buffer{}, d, "typo.example", "mail.example.com"); !errors.Is(err, errUnknownDomain) {
		t.Errorf("dns-records on an unknown domain: err %v, want errUnknownDomain", err)
	}
	if err := enableDKIM(d, "example.com"); err == nil {
		t.Error("enable-dkim with no key succeeded; it must ask for generate-dkim first")
	}
	if d.keyWrites != 0 {
		t.Errorf("%d keys written for refused commands", d.keyWrites)
	}
}

// TestSetAVScanTakesOnlyOnOrOff: any other word must be refused, not read as off.
func TestSetAVScanTakesOnlyOnOrOff(t *testing.T) {
	d := newFakeSetupDir("example.com")
	if err := setAVScan(d, "example.com", "on"); err != nil || !d.av["example.com"] {
		t.Fatalf("on: err %v scan %v", err, d.av["example.com"])
	}
	if err := setAVScan(d, "example.com", "yes"); err == nil {
		t.Error(`"yes" was accepted; only on and off are states`)
	}
	if !d.av["example.com"] {
		t.Error("a refused state changed the setting")
	}
}

// TestSetTLSACMENeedsTheTermsAndAnAddress follows the CA's requirement that its
// terms are accepted explicitly, and refuses a contact that is not an address.
func TestSetTLSACMENeedsTheTermsAndAnAddress(t *testing.T) {
	d := newFakeSetupDir()
	if err := setTLSACME(d, "ops@example.com", false); err == nil || d.tls != nil {
		t.Fatalf("without --agree-tos: err %v, stored %+v; want refused and nothing stored", err, d.tls)
	}
	for _, bad := range []string{"", "ops", "@example.com", "ops@", "a@b@c"} {
		if err := setTLSACME(d, bad, true); err == nil {
			t.Errorf("email %q accepted", bad)
		}
	}
	if d.tls != nil {
		t.Fatalf("a refused call stored %+v", d.tls)
	}
	if err := setTLSACME(d, "ops@example.com", true); err != nil {
		t.Fatal(err)
	}
	if d.tls.Mode != "acme" || d.tls.ACMEEmail != "ops@example.com" || !d.tls.ACMEAgreed {
		t.Errorf("stored %+v, want acme mode with the email and agreement", *d.tls)
	}
}

// TestDNSRecordsPrintsTheDKIMRecordItGenerated: the printed list carries the
// generated key's record at <selector>._domainkey.<domain>, one tab-separated
// type, name and value per line.
func TestDNSRecordsPrintsTheDKIMRecordItGenerated(t *testing.T) {
	d := newFakeSetupDir("example.com")
	if _, err := generateDKIM(d, "example.com", fakeKey); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeDNSRecords(&out, d, "example.com", "mx.example.net"); err != nil {
		t.Fatal(err)
	}
	want := "TXT\thermex._domainkey.example.com\tv=DKIM1; k=rsa; p=AAAA\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("output lacks the DKIM line %q:\n%s", want, out.String())
	}
	if !strings.Contains(out.String(), "MX\texample.com\t10 mx.example.net\n") {
		t.Errorf("output lacks the MX line:\n%s", out.String())
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if strings.Count(line, "\t") != 2 {
			t.Errorf("line %q is not type, name and value", line)
		}
	}
}
