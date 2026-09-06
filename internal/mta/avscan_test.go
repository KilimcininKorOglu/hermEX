package mta

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hermex/internal/antivirus"
	"hermex/internal/directory"
)

// fakeClamd starts a minimal clamd that drains one INSTREAM and replies the given
// record, for every connection until the test ends.
func fakeClamd(t *testing.T, reply string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				cmd := make([]byte, len("zINSTREAM\x00"))
				if _, err := io.ReadFull(c, cmd); err != nil {
					return
				}
				var hdr [4]byte
				for {
					if _, err := io.ReadFull(c, hdr[:]); err != nil {
						return
					}
					n := binary.BigEndian.Uint32(hdr[:])
					if n == 0 {
						break
					}
					if _, err := io.CopyN(io.Discard, c, int64(n)); err != nil {
						return
					}
				}
				_, _ = io.WriteString(c, reply)
			}(c)
		}
	}()
	return "tcp://" + ln.Addr().String()
}

// fakeAVDir is an in-memory avDirectory recording quarantine inserts.
type fakeAVDir struct {
	inbound, outbound bool
	domainID          int64
	known             bool
	quarantined       []directory.QuarantineEntry
	// adminsErr makes the admin lookup fail, the case a virus event must still
	// record rather than treat as "this domain has no admins".
	adminsErr error
}

func (f *fakeAVDir) GetDomainAVScan(string) (bool, bool, error)   { return f.inbound, f.outbound, nil }
func (f *fakeAVDir) DomainID(string) (int64, bool, error)         { return f.domainID, f.known, nil }
func (f *fakeAVDir) DomainOrgAdminEmails(int64) ([]string, error) { return nil, f.adminsErr }
func (f *fakeAVDir) QuarantineMessage(e directory.QuarantineEntry) (int64, error) {
	f.quarantined = append(f.quarantined, e)
	return int64(len(f.quarantined)), nil
}

func TestScanMessage(t *testing.T) {
	clean := fakeClamd(t, "stream: OK\x00")
	found := fakeClamd(t, "stream: Eicar-Test-Signature FOUND\x00")
	const dead = "tcp://127.0.0.1:1" // unreachable

	accounts := directory.StaticAccounts{} // empty: notice delivery is a no-op
	tmp := t.TempDir()
	quarPath := func(id int64) string { return filepath.Join(tmp, fmt.Sprintf("%d.eml", id)) }
	raw := []byte("From: evil@spam.example\r\nSubject: hi\r\n\r\nbody")
	when := time.Unix(1000, 0)

	set := func(addr string, fd *fakeAVDir) {
		sc, err := antivirus.New(addr)
		mustNoErr(t, "build the scanner", err)
		SetScanner(sc, fd, quarPath, "mail.test", nil)
	}
	t.Cleanup(func() { SetScanner(nil, nil, nil, "", nil) })
	inbound := func() avDecision {
		return scanMessage(accounts, avInboundSMTP, "e@x", []string{"v@acme.test"}, raw, when)
	}
	submission := func() avDecision {
		return scanMessage(accounts, avSubmission, "s@acme.test", []string{"ext@far.test"}, raw, when)
	}

	// Toggles off: never scanned.
	set(found, &fakeAVDir{domainID: 7, known: true})
	wantEq(t, "the decision with the toggles off", inbound(), avProceed)

	// Inbound on + clean: proceed.
	set(clean, &fakeAVDir{inbound: true, domainID: 7, known: true})
	wantEq(t, "the decision on a clean message", inbound(), avProceed)

	// Inbound on + FOUND: quarantined (direction inbound, scope 7), eml written.
	fin := &fakeAVDir{inbound: true, domainID: 7, known: true}
	set(found, fin)
	wantEq(t, "the decision on an inbound hit", inbound(), avHandled)
	entry := onlyQuarantine(t, "the inbound quarantine", fin)
	wantEq(t, "the quarantine direction", entry.Direction, "inbound")
	wantEq(t, "the quarantine domain", entry.DomainID, int64(7))
	wantEq(t, "the quarantined virus", entry.VirusName, "Eicar-Test-Signature")
	if _, err := os.Stat(quarPath(1)); err != nil {
		t.Errorf("eml not written: %v", err)
	}

	// Inbound + clamd down: temp-fail (sender retries).
	set(dead, &fakeAVDir{inbound: true, domainID: 7, known: true})
	wantEq(t, "the decision when clamd is down inbound", inbound(), avTempFail)

	// Submission + clamd down: fail open.
	set(dead, &fakeAVDir{outbound: true, domainID: 7, known: true})
	wantEq(t, "the decision when clamd is down on submission", submission(), avProceed)

	// Submission + sender outbound + FOUND: handled, direction outbound.
	fout := &fakeAVDir{outbound: true, domainID: 9, known: true}
	set(found, fout)
	wantEq(t, "the decision on an outbound hit", submission(), avHandled)
	wantEq(t, "the outbound quarantine direction", onlyQuarantine(t, "the outbound quarantine", fout).Direction, "outbound")

	// No scanner installed: proceed.
	SetScanner(nil, nil, nil, "", nil)
	wantEq(t, "the decision with no scanner", inbound(), avProceed)
}

// onlyQuarantine requires the fake directory to hold exactly one quarantine
// record and returns it.
func onlyQuarantine(t *testing.T, what string, fd *fakeAVDir) directory.QuarantineEntry {
	t.Helper()
	if len(fd.quarantined) != 1 {
		t.Fatalf("%s = %+v, want one record", what, fd.quarantined)
	}
	return fd.quarantined[0]
}

// TestDeliverAndRelayBlocksVirus proves an outbound virus is quarantined and the
// submission returns the terminal ErrVirusBlocked (so callers skip the Sent copy
// and the send-later sweep drops the scheduled message instead of looping).
func TestDeliverAndRelayBlocksVirus(t *testing.T) {
	found := fakeClamd(t, "stream: Win.Test.EICAR FOUND\x00")
	tmp := t.TempDir()
	quarPath := func(id int64) string { return filepath.Join(tmp, fmt.Sprintf("%d.eml", id)) }
	sc, err := antivirus.New(found)
	if err != nil {
		t.Fatal(err)
	}
	fd := &fakeAVDir{outbound: true, domainID: 3, known: true}
	SetScanner(sc, fd, quarPath, "mail.test", nil)
	t.Cleanup(func() { SetScanner(nil, nil, nil, "", nil) })

	raw := []byte("From: u@acme.test\r\nSubject: x\r\n\r\nbody")
	_, err = DeliverAndRelay(directory.StaticAccounts{}, nil, "u@acme.test", []string{"ext@far.test"}, raw, time.Unix(1000, 0))
	if !errors.Is(err, ErrVirusBlocked) {
		t.Fatalf("err = %v, want ErrVirusBlocked", err)
	}
	if len(fd.quarantined) != 1 || fd.quarantined[0].Direction != "outbound" {
		t.Fatalf("quarantine = %+v", fd.quarantined)
	}
	var term interface{ TerminalDelivery() bool }
	if !errors.As(err, &term) || !term.TerminalDelivery() {
		t.Error("ErrVirusBlocked should be a terminal delivery error")
	}
}
