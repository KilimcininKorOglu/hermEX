package mta

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/antivirus"
	"hermex/internal/directory"
	"hermex/internal/logging"
)

// TestQuarantineRecordsAdminLookupFailure is the swallowed-failure defect. A failed
// admin lookup returns no admins, which is indistinguishable from a domain that has
// none, so a directory outage silently skipped every administrator alert for a virus
// event while the log still showed a clean quarantine.
func TestQuarantineRecordsAdminLookupFailure(t *testing.T) {
	found := fakeClamd(t, "stream: Eicar-Test-Signature FOUND\x00")
	tmp := t.TempDir()
	quarPath := func(id int64) string { return filepath.Join(tmp, fmt.Sprintf("%d.eml", id)) }

	sc, err := antivirus.New(found)
	if err != nil {
		t.Fatal(err)
	}
	fd := &fakeAVDir{inbound: true, domainID: 7, known: true, adminsErr: errors.New("database unavailable")}
	sink := &captureSink{}
	SetScanner(sc, fd, quarPath, "mail.test", logging.New(sink))
	t.Cleanup(func() { SetScanner(nil, nil, nil, "", nil) })

	raw := []byte("From: evil@spam.example\r\nSubject: hi\r\n\r\nbody")
	if d := scanMessage(directory.StaticAccounts{}, avInboundSMTP, "e@x", []string{"v@acme.test"}, raw, time.Unix(1000, 0)); d != avHandled {
		t.Fatalf("virus hit gave %d, want avHandled", d)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, e := range sink.events {
		if e.Name == "av.admins.fail" {
			return
		}
	}
	t.Errorf("the admin lookup failure was never recorded; events = %+v", sink.events)
}
