package rpchttp

import (
	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// RFR (address-book referral) interface identity ([MS-OXABREF] 2.1): the service
// a desktop Outlook may query first to learn which directory server to bind for
// the GAL. hermEX serves one NSPI server on the same host, so every referral
// resolves to the configured hostname.
var (
	// RFRUUID is the RFR interface UUID 1544f5e0-613c-11d1-93df-00c04fd7bd09.
	RFRUUID = mapi.GUID{
		Data1: 0x1544F5E0, Data2: 0x613C, Data3: 0x11D1,
		Data4: [8]byte{0x93, 0xDF, 0x00, 0xC0, 0x4F, 0xD7, 0xBD, 0x09},
	}
	// RFRVersion is the interface version 1.0 (wire u32 1).
	RFRVersion uint32 = 1
)

// RFR opnums ([MS-OXABREF] 3.1.4).
const (
	opRfrGetNewDSA           uint16 = 0
	opRfrGetFQDNFromServerDN uint16 = 1
)

// RFR answers address-book referral requests with the configured hostname as the
// directory server. Its Handle method is registered on a Dispatcher.
type RFR struct{ hostname string }

// NewRFR returns an RFR referral stub that points every client at hostname.
func NewRFR(hostname string) *RFR { return &RFR{hostname: hostname} }

// Handle is the IfaceHandler the dispatcher calls for an RFR request. The GAL
// referral is the same for every authenticated caller, so the session is unused.
func (r *RFR) Handle(_ *Session, opnum uint16, stub []byte) ([]byte, uint32) {
	switch opnum {
	case opRfrGetNewDSA:
		return r.getNewDSA(stub)
	case opRfrGetFQDNFromServerDN:
		return r.getFQDN(stub)
	default:
		return nil, ndr.FaultOpRngError
	}
}

// pullRfrString reads a conformant-varying 8-bit string (max_count + offset +
// actual + bytes), validating the NDR invariants and a length cap.
func pullRfrString(p *ndr.Pull, capLen uint32) (string, error) {
	size, err := p.Uint32()
	if err != nil {
		return "", err
	}
	offset, err := p.Uint32()
	if err != nil {
		return "", err
	}
	length, err := p.Uint32()
	if err != nil {
		return "", err
	}
	if offset != 0 || length > size || length > capLen {
		return "", ndr.ErrFormat
	}
	b, err := p.Raw(int(length))
	if err != nil {
		return "", err
	}
	return trimNUL(b), nil
}

// pushRfrString writes a non-null conformant-varying 8-bit string (max_count +
// offset + actual + NUL-terminated bytes); the caller emits the referent first.
func pushRfrString(p *ndr.Push, s string) {
	b := append([]byte(s), 0)
	n := uint32(len(b))
	p.Uint32(n)
	p.Uint32(0)
	p.Uint32(n)
	p.Raw(b)
}

// getNewDSA handles RfrGetNewDSA (opnum 0): it reads (and discards) the user DN
// and the two optional [in,out] server-hint pointers, then returns this host as
// the directory server to bind. OUT: a null ppszUnused, the server string behind
// a double unique pointer, then the result.
func (r *RFR) getNewDSA(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if _, err := p.Uint32(); err != nil { // flags
		return nil, ndr.FaultNdr
	}
	if _, err := pullRfrString(p, 1024); err != nil { // pUserDN
		return nil, ndr.FaultNdr
	}
	// ppszUnused and ppszServer are [in,out] pointers to pointers to strings; read
	// and discard both (the server identity is derived from the host, not the
	// client's hints).
	for range 2 {
		outer, err := p.Uint32()
		if err != nil {
			return nil, ndr.FaultNdr
		}
		if outer == 0 {
			continue
		}
		inner, err := p.Uint32()
		if err != nil {
			return nil, ndr.FaultNdr
		}
		if inner == 0 {
			continue
		}
		if _, err := pullRfrString(p, 256); err != nil {
			return nil, ndr.FaultNdr
		}
	}
	out := ndr.NewPush()
	out.UniquePtr(false) // ppszUnused: always null
	r.pushServer(out)
	out.Uint32(ecSuccess)
	return out.Bytes(), 0
}

// getFQDN handles RfrGetFQDNFromServerDN (opnum 1): it reads (and discards) the
// mailbox-server DN and returns this host's FQDN. OUT: the FQDN behind a single
// unique pointer, then the result.
func (r *RFR) getFQDN(stub []byte) ([]byte, uint32) {
	p := ndr.NewPull(stub)
	if _, err := p.Uint32(); err != nil { // flags
		return nil, ndr.FaultNdr
	}
	cb, err := p.Uint32()
	if err != nil {
		return nil, ndr.FaultNdr
	}
	if cb < 10 || cb > 1024 {
		return nil, ndr.FaultNdr
	}
	if _, err := pullRfrString(p, 1024); err != nil { // mbserverdn
		return nil, ndr.FaultNdr
	}
	out := ndr.NewPush()
	if r.hostname == "" {
		out.UniquePtr(false)
	} else {
		out.UniquePtr(true)
		pushRfrString(out, r.hostname)
	}
	out.Uint32(ecSuccess)
	return out.Bytes(), 0
}

// pushServer emits the ppszServer OUT value: a null double pointer when no
// hostname is configured, else the outer + inner referents and the host string.
func (r *RFR) pushServer(out *ndr.Push) {
	if r.hostname == "" {
		out.UniquePtr(false)
		return
	}
	out.UniquePtr(true) // outer pointer
	out.UniquePtr(true) // inner string pointer
	pushRfrString(out, r.hostname)
}
