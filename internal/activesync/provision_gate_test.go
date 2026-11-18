package activesync

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/easpolicy"
	"hermex/internal/logging"
)

// TestPolicyGateRefusesAnUnreadableDeviceRecord proves the command gate fails
// closed when it cannot read which policy a device acknowledged: under a
// configured policy the command is refused, as a server error rather than a 449,
// because a Provision that cannot write the acknowledgement either would answer
// the 449 in a loop. The failure is recorded. The policy here comes from the
// directory and the mailbox does not exist, so the device record cannot be read.
func TestPolicyGateRefusesAnUnreadableDeviceRecord(t *testing.T) {
	accs := policyAccounts{
		StaticAccounts: directory.StaticAccounts{
			testUser: {Password: testPass, MailboxPath: filepath.Join(t.TempDir(), "missing")},
		},
		def: easpolicy.Policy{"DevicePasswordEnabled": 1},
	}
	srv := NewServer(accs, accs, "mail.hermex.test")
	sink := &policySink{}
	srv.Logger = logging.New(sink)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	code := commandStatusWithKey(t, ts, "FolderSync", folderSync(), easpolicy.Key(accs.def))
	if code != http.StatusServiceUnavailable {
		t.Errorf("FolderSync with an unreadable device record: status %d, want 503", code)
	}
	for _, e := range sink.events {
		if e.Name == "device.record.fail" {
			return
		}
	}
	t.Error("an unreadable device record recorded no device.record.fail event")
}
