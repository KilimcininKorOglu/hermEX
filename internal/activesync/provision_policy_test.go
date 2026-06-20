package activesync

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/easpolicy"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// TestProvisionTokenCoverage guards that every modeled policy field has a WBXML token,
// so a field added to easpolicy without a token is caught here rather than silently
// dropped from the served document.
func TestProvisionTokenCoverage(t *testing.T) {
	for _, f := range easpolicy.Fields {
		if _, ok := provisionToken[f.Name]; !ok {
			t.Errorf("policy field %q has no WBXML token", f.Name)
		}
	}
}

// policyAccounts is a StaticAccounts that also supplies a server-wide default policy,
// so the Provision handler's optional defaultSyncPolicyProvider assertion fires.
type policyAccounts struct {
	directory.StaticAccounts
	def easpolicy.Policy
}

func (p policyAccounts) GetDefaultSyncPolicy() (easpolicy.Policy, error) { return p.def, nil }

// TestProvisionServesMergedPolicy proves the Provision document carries the server
// default with the mailbox override merged on top: an inherited field comes from the
// default, an overridden field takes the mailbox value, and an override-only field is
// added — the device is told exactly the resolved policy.
func TestProvisionServesMergedPolicy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mbox")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncPolicy(easpolicy.Policy{"MinDevicePasswordLength": 8, "AllowCamera": 0}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	accs := policyAccounts{
		StaticAccounts: directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: dir}},
		def:            easpolicy.Policy{"DevicePasswordEnabled": 1, "MinDevicePasswordLength": 4},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	defer ts.Close()

	phase1 := wbxml.Elem(wbxml.PVProvision,
		wbxml.Elem(wbxml.PVPolicies, wbxml.Elem(wbxml.PVPolicy,
			wbxml.Str(wbxml.PVPolicyType, "MS-EAS-Provisioning-WBXML"))))
	_, root := postCommand(t, ts, "Provision", phase1)

	doc := root.Child(wbxml.PVPolicies).Child(wbxml.PVPolicy).Child(wbxml.PVData).Child(wbxml.PVEASProvisionDoc)
	if doc == nil {
		t.Fatal("phase 1 carried no EAS provision document")
	}
	if got := doc.ChildText(wbxml.PVDevicePasswordEnabled); got != "1" {
		t.Errorf("DevicePasswordEnabled = %q, want 1 (inherited from default)", got)
	}
	if got := doc.ChildText(wbxml.PVMinDevicePasswordLength); got != "8" {
		t.Errorf("MinDevicePasswordLength = %q, want 8 (override wins over default 4)", got)
	}
	if got := doc.ChildText(wbxml.PVAllowCamera); got != "0" {
		t.Errorf("AllowCamera = %q, want 0 (added by override)", got)
	}
}
