package activesync

import (
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/easpolicy"
	"hermex/internal/logging"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// policySink collects the events a provisioning pass emits.
type policySink struct{ events []logging.Event }

func (s *policySink) Write(e logging.Event) { s.events = append(s.events, e) }

// failingPolicyAccounts answers both policy layers with an error, the state a
// directory outage puts the server in.
type failingPolicyAccounts struct {
	directory.StaticAccounts
}

func (failingPolicyAccounts) GetDefaultSyncPolicy() (easpolicy.Policy, error) {
	return nil, errors.New("directory unreachable")
}
func (failingPolicyAccounts) GetDomainSyncPolicy(string) (easpolicy.Policy, error) {
	return nil, errors.New("directory unreachable")
}

// TestPolicyReadFailureIsLogged is the defect. A failed policy read contributes an
// empty layer, an all-empty merge carries the baseline key, and the enforcement
// check treats that key as "this device needs no policy". So a directory outage
// quietly stops challenging devices to re-provision, and nothing said so.
func TestPolicyReadFailureIsLogged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	accs := failingPolicyAccounts{
		StaticAccounts: directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}},
	}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sink := &policySink{}
	srv.Logger = logging.New(sink)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	phase1 := wbxml.Elem(wbxml.PVProvision,
		wbxml.Elem(wbxml.PVPolicies, wbxml.Elem(wbxml.PVPolicy,
			wbxml.Str(wbxml.PVPolicyType, "MS-EAS-Provisioning-WBXML"))))
	postCommand(t, ts, "Provision", phase1)

	var layers []string
	for _, e := range sink.events {
		if e.Name == "policy.read.fail" {
			if e.Level != logging.LevelWarn {
				t.Errorf("event level = %s, want warn", e.Level)
			}
			if got, _ := e.Fields["error"].(string); got == "" {
				t.Error("the event carries no error text")
			}
			if e.User != testUser {
				t.Errorf("event user = %q, want the account whose policy failed", e.User)
			}
			layer, _ := e.Fields["layer"].(string)
			layers = append(layers, layer)
		}
	}
	if len(layers) < 2 || !strings.Contains(strings.Join(layers, ","), "default") ||
		!strings.Contains(strings.Join(layers, ","), "domain") {
		t.Errorf("logged layers = %v, want both the default and the domain read reported", layers)
	}
}

// TestWipeReadFailureIsLogged proves a remote-wipe state that cannot be read is
// recorded: the device is then served no wipe directive, so without the event a
// wipe the operator ordered would never reach it and nothing would say so. A
// mailbox that does not exist makes the read fail.
func TestWipeReadFailureIsLogged(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-provisioned")
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: missing}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sink := &policySink{}
	srv.Logger = logging.New(sink)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	phase1 := wbxml.Elem(wbxml.PVProvision,
		wbxml.Elem(wbxml.PVPolicies, wbxml.Elem(wbxml.PVPolicy,
			wbxml.Str(wbxml.PVPolicyType, "MS-EAS-Provisioning-WBXML"))))
	postCommand(t, ts, "Provision", phase1)

	for _, e := range sink.events {
		if e.Name == "wipe.read.fail" {
			if e.User != testUser || e.Fields["device"] != "dev1" {
				t.Errorf("wipe.read.fail user %q device %v, want %s/dev1", e.User, e.Fields["device"], testUser)
			}
			return
		}
	}
	t.Error("an unreadable wipe state recorded no wipe.read.fail event")
}

// TestACleanPolicyReadLogsNothing is the negative control: provisioning runs on
// every device sync, so a pass that logs when nothing is wrong would bury the log
// store and make the real failure unfindable.
func TestACleanPolicyReadLogsNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	accs := policyAccounts{
		StaticAccounts: directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}},
		def:            easpolicy.Policy{"DevicePasswordEnabled": 1},
	}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sink := &policySink{}
	srv.Logger = logging.New(sink)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	phase1 := wbxml.Elem(wbxml.PVProvision,
		wbxml.Elem(wbxml.PVPolicies, wbxml.Elem(wbxml.PVPolicy,
			wbxml.Str(wbxml.PVPolicyType, "MS-EAS-Provisioning-WBXML"))))
	_, root := postCommand(t, ts, "Provision", phase1)

	// The pass must have read the policy, or the silence below proves nothing.
	doc := root
	for _, tag := range []wbxml.Tag{wbxml.PVPolicies, wbxml.PVPolicy, wbxml.PVData, wbxml.PVEASProvisionDoc} {
		if doc = doc.Child(tag); doc == nil {
			t.Fatalf("Provision answered no policy document (missing tag %v)", tag)
		}
	}
	if got := doc.ChildText(wbxml.PVDevicePasswordEnabled); got != "1" {
		t.Fatalf("DevicePasswordEnabled = %q, want the configured 1", got)
	}
	for _, e := range sink.events {
		if e.Name == "policy.read.fail" {
			t.Errorf("a clean policy read logged a failure: %+v", e.Fields)
		}
	}
}
