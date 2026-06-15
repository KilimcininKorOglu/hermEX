package rpchttp

import (
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
	if unused, _ := q.Uint32(); unused != 0 {
		t.Errorf("ppszUnused referent = %#x, want null", unused)
	}
	outer, _ := q.Uint32()
	inner, _ := q.Uint32()
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
	if result, _ := q.Uint32(); result != ecSuccess {
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
	ref, _ := q.Uint32()
	if ref == 0 {
		t.Fatal("FQDN referent absent")
	}
	fqdn, err := pullRfrString(q, 1024)
	if err != nil {
		t.Fatalf("fqdn string: %v", err)
	}
	if fqdn != rfrHost {
		t.Errorf("fqdn = %q, want %q", fqdn, rfrHost)
	}
	if result, _ := q.Uint32(); result != ecSuccess {
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
}
