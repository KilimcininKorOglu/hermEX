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
	postCommand(t, ts, "Provision", phase1)

	for _, e := range sink.events {
		if e.Name == "policy.read.fail" {
			t.Errorf("a clean policy read logged a failure: %+v", e.Fields)
		}
	}
}
