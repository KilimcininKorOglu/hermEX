package rpchttp

import (
	"strings"
	"testing"

	"hermex/internal/ndr"
)

const rfrHost = "mail.hermex.test"

// TestRfrGetNewDSA proves RfrGetNewDSA returns the configured host as the
// directory server behind the ppszServer double pointer, with a null ppszUnused.
func TestRfrGetNewDSA(t *testing.T) {
	r := NewRFR(rfrHost)
	p := ndr.NewPush()
	p.Uint32(0)                            // flags
	pushRfrString(p, "/o=hermex/cn=alice") // pUserDN
	p.UniquePtr(false)                     // ppszUnused hint: null
	p.UniquePtr(false)                     // ppszServer hint: null
	out, fault := r.Handle(nil, opRfrGetNewDSA, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	if unused := mustU32(t, q, "ppszUnused referent"); unused != 0 {
		t.Errorf("ppszUnused referent = %#x, want null", unused)
	}
	outer := mustU32(t, q, "outer server pointer")
	inner := mustU32(t, q, "inner server pointer")
	if outer == 0 || inner == 0 {
		t.Fatalf("ppszServer pointers = (%#x, %#x), want both present", outer, inner)
	}
	server, err := pullRfrString(q, 256)
	if err != nil {
		t.Fatalf("server string: %v", err)
	}
	if server != rfrHost {
		t.Errorf("server = %q, want %q", server, rfrHost)
	}
	if result := mustU32(t, q, "result"); result != ecSuccess {
		t.Errorf("result = %#x, want ecSuccess", result)
	}
}

// TestRfrGetFQDN proves RfrGetFQDNFromServerDN returns the host's FQDN.
func TestRfrGetFQDN(t *testing.T) {
	r := NewRFR(rfrHost)
	p := ndr.NewPush()
	p.Uint32(0)  // flags
	p.Uint32(32) // cb (10..1024)
	pushRfrString(p, "/o=hermex/ou=Exchange/cn=Servers/cn=HOST")
	out, fault := r.Handle(nil, opRfrGetFQDNFromServerDN, p.Bytes())
	if fault != 0 {
		t.Fatalf("fault %#x", fault)
	}
	q := ndr.NewPull(out)
	if ref := mustU32(t, q, "FQDN referent"); ref == 0 {
		t.Fatal("FQDN referent absent")
	}
	fqdn, err := pullRfrString(q, 1024)
	if err != nil {
		t.Fatalf("fqdn string: %v", err)
	}
	if fqdn != rfrHost {
		t.Errorf("fqdn = %q, want %q", fqdn, rfrHost)
	}
	if result := mustU32(t, q, "result"); result != ecSuccess {
		t.Errorf("result = %#x, want ecSuccess", result)
	}
}

// TestRfrGetFQDNRejectsShortCb proves the cb bounds check ([MS-OXABREF]: 10..1024)
// faults a too-small length rather than serving a malformed request.
func TestRfrGetFQDNRejectsShortCb(t *testing.T) {
	r := NewRFR(rfrHost)
	p := ndr.NewPush()
	p.Uint32(0) // flags
	p.Uint32(4) // cb below the minimum
	pushRfrString(p, "/cn=x")
	if _, fault := r.Handle(nil, opRfrGetFQDNFromServerDN, p.Bytes()); fault != ndr.FaultNdr {
		t.Errorf("short cb fault = %#x, want FaultNdr", fault)
	}
}

// TestRfrUnknownOpnum proves an opnum outside the two referral calls faults.
func TestRfrUnknownOpnum(t *testing.T) {
	r := NewRFR(rfrHost)
	if _, fault := r.Handle(nil, 9, nil); fault != ndr.FaultOpRngError {
		t.Errorf("unknown opnum fault = %#x, want op_rng_error", fault)
	}
	wantEq(t, r.hostname, rfrHost, "hostname after unknown opnum")
}

// pushBadRfrString writes a conformant-varying string header with caller-chosen
// size, offset, and length followed by body bytes.
func pushBadRfrString(p *ndr.Push, size, offset, length uint32, body []byte) {
	p.Uint32(size)
	p.Uint32(offset)
	p.Uint32(length)
	p.Raw(body)
}

// buildHostileNewDSAStub builds a malformed RfrGetNewDSA stub with custom DN header.
func buildHostileNewDSAStub(size, offset, length uint32, body []byte) []byte {
	p := ndr.NewPush()
	p.Uint32(0) // flags
	pushBadRfrString(p, size, offset, length, body)
	p.UniquePtr(false)
	p.UniquePtr(false)
	return p.Bytes()
}

// buildHostileFQDNStub builds a malformed RfrGetFQDNFromServerDN stub.
func buildHostileFQDNStub(cb, size, offset, length uint32, body []byte) []byte {
	p := ndr.NewPush()
	p.Uint32(0) // flags
	p.Uint32(cb)
	pushBadRfrString(p, size, offset, length, body)
	return p.Bytes()
}

// TestRfrRejectsHostileStubs proves RfrGetNewDSA and RfrGetFQDNFromServerDN
// refuse truncated, out-of-bounds, and oversized NDR inputs, ignore forged
// server hints, and leave RFR state unchanged.
func TestRfrRejectsHostileStubs(t *testing.T) {
	r := NewRFR(rfrHost)
	oversized := append([]byte(strings.Repeat("a", 1025)), 0)
	overHint := append([]byte(strings.Repeat("b", 257)), 0)

	hintStub := ndr.NewPush()
	hintStub.Uint32(0)
	pushRfrString(hintStub, "/o=hermex/cn=alice")
	hintStub.UniquePtr(false)
	hintStub.UniquePtr(true)
	hintStub.UniquePtr(true)
	pushBadRfrString(hintStub, uint32(len(overHint)), 0, uint32(len(overHint)), overHint)

	badCases := []struct {
		name  string
		opnum uint16
		stub  []byte
	}{
		{"newDSA truncated", opRfrGetNewDSA, []byte{0, 0}},
		{"newDSA non-zero offset", opRfrGetNewDSA, buildHostileNewDSAStub(4, 1, 2, []byte("a\x00"))},
		{"newDSA length > size", opRfrGetNewDSA, buildHostileNewDSAStub(2, 0, 4, []byte("abc\x00"))},
		{"newDSA oversized DN", opRfrGetNewDSA, buildHostileNewDSAStub(uint32(len(oversized)), 0, uint32(len(oversized)), oversized)},
		{"newDSA oversized hint", opRfrGetNewDSA, hintStub.Bytes()},
		{"fqdn truncated", opRfrGetFQDNFromServerDN, []byte{0}},
		{"fqdn cb below min", opRfrGetFQDNFromServerDN, buildHostileFQDNStub(9, 6, 0, 6, []byte("/cn=x\x00"))},
		{"fqdn cb above max", opRfrGetFQDNFromServerDN, buildHostileFQDNStub(1025, 6, 0, 6, []byte("/cn=x\x00"))},
		{"fqdn non-zero offset", opRfrGetFQDNFromServerDN, buildHostileFQDNStub(32, 6, 1, 6, []byte("/cn=x\x00"))},
		{"fqdn oversized DN", opRfrGetFQDNFromServerDN, buildHostileFQDNStub(32, uint32(len(oversized)), 0, uint32(len(oversized)), oversized)},
	}
	for _, tc := range badCases {
		out, fault := r.Handle(nil, tc.opnum, tc.stub)
		wantEq(t, fault, uint32(ndr.FaultNdr), tc.name+" fault")
		wantEq(t, len(out), 0, tc.name+" output length")
	}

	// Forged ppszServer hint is ignored; configured hostname is returned and intact.
	forged := ndr.NewPush()
	forged.Uint32(0)
	pushRfrString(forged, "/o=hermex/cn=alice")
	forged.UniquePtr(false)
	forged.UniquePtr(true)
	forged.UniquePtr(true)
	pushRfrString(forged, "evil.attacker.test")
	out, fault := r.Handle(nil, opRfrGetNewDSA, forged.Bytes())
	wantEq(t, fault, uint32(0), "forged-hint fault")
	q := ndr.NewPull(out)
	wantEq(t, mustU32(t, q, "ppszUnused"), uint32(0), "ppszUnused")
	wantTrue(t, mustU32(t, q, "outer") != 0, "outer pointer")
	wantTrue(t, mustU32(t, q, "inner") != 0, "inner pointer")
	got, err := pullRfrString(q, 256)
	mustNoErr(t, err, "pull server after forged hint")
	wantEq(t, got, rfrHost, "server after forged hint")
	wantEq(t, mustU32(t, q, "result"), uint32(ecSuccess), "result after forged hint")
	wantEq(t, r.hostname, rfrHost, "hostname unchanged")
}
