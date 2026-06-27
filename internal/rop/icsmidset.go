package rop

import "hermex/internal/ext"

// ropSetLocalReplicaMidsetDeleted is the ICS local-replica id-reclamation ROP
// ([MS-OXCROPS] 2.2.13.13).
const ropSetLocalReplicaMidsetDeleted uint8 = 0x93

// ropSetLocalReplicaMidsetDeleted handles RopSetLocalReplicaMidsetDeleted
// ([MS-OXCFXICS] 2.2.3.2.4.4): the client reports id ranges it has locally deleted
// so the server may reclaim them into its replica's deleted-id set. hermEX is a
// single-store model with no peer replica whose midset must be tracked, so there is
// nothing to record; the request is accepted (its LongTermIdRange payload consumed
// so the batch stays framed) and answered ecSuccess.
//
// The request body is a uint16 byte-count prefix followed by that many bytes of
// (uint32 range count + LongTermIdRange entries); consuming exactly the prefixed
// span keeps the ROP batch parser aligned without decoding ranges hermEX discards.
func (s *Session) ropSetLocalReplicaMidsetDeleted(p *ext.Pull, out *ext.Push, _ []uint32, hindex uint8) bool {
	dataSize, e1 := p.Uint16()
	if e1 != nil {
		return false
	}
	if _, err := p.Raw(int(dataSize)); err != nil {
		return false
	}
	out.Uint8(ropSetLocalReplicaMidsetDeleted)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	return true
}
