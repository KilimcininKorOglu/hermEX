// Package ics implements the MS-OXCFXICS incremental change synchronization
// primitives: the IDSET/GLOBSET model in this file and globset.go. An IDSET is
// a set of message or folder ids, grouped by replica, serialized as a compact
// GLOBSET command stream. It is the basis for the ics_state idsets (given/seen)
// the client and server exchange to compute a sync delta.
//
// All GC-value packing reuses internal/mapi (ValueToGC/GCToValue are the 48-bit
// big-endian GLOBCNT codec). The FastTransfer stream codec, ics_state, and the
// download/upload contexts build on this package in later increments.
package ics

import (
	"encoding/binary"
	"fmt"

	"hermex/internal/mapi"
)

// Form is the IDSET keying/serialization form ([MS-OXCFXICS] 2.2.2.x). The low
// bit marks the packed (just-serialized, immutable) form; the high bit marks
// GUID-keyed (vs replid-keyed) replica blocks.
type Form uint8

const (
	FormIDPacked   Form = 0x41 // replid-keyed, serialized
	FormIDLoose    Form = 0x42 // replid-keyed, mutable/queryable
	FormGUIDPacked Form = 0x81 // replguid-keyed, serialized
	FormGUIDLoose  Form = 0x82 // replguid-keyed, mutable/queryable
)

// packed reports the serialized form, which rejects mutation (Append/Remove/
// Concatenate) and is not directly queryable — call Convert first.
func (f Form) packed() bool { return uint8(f)&0x01 != 0 }

// ReplicaMapper resolves between a 16-bit replica id and its replica GUID. The
// GUID forms need it: Convert maps a deserialized GUID block back to a replid,
// and Serialize maps a replid to the GUID written on the wire. ics_state
// supplies an implementation backed by the logon (a later increment).
type ReplicaMapper interface {
	ToGUID(replid uint16) (mapi.GUID, bool)
	ToID(guid mapi.GUID) (uint16, bool)
}

// replNode is one replica's id ranges. The loose forms key by replid; replguid
// is populated only while a GUID block is packed (pre-Convert) or resolved by
// Convert.
type replNode struct {
	replid   uint16
	replguid mapi.GUID
	ranges   rangeSet
}

// IDSet is a replica-grouped set of ids in one of the four Forms.
type IDSet struct {
	form   Form
	repls  []replNode
	mapper ReplicaMapper
}

// NewIDSet returns an empty IDSet in the given form. mapper may be nil for the
// replid (id_*) forms; the GUID forms require it for Serialize/Convert.
func NewIDSet(form Form, mapper ReplicaMapper) *IDSet {
	return &IDSet{form: form, mapper: mapper}
}

// Form returns the current form.
func (s *IDSet) Form() Form { return s.form }

// Empty reports whether the set holds no ranges.
func (s *IDSet) Empty() bool {
	for i := range s.repls {
		if !s.repls[i].ranges.empty() {
			return false
		}
	}
	return true
}

func (s *IDSet) findOrCreate(replid uint16) *replNode {
	for i := range s.repls {
		if s.repls[i].replid == replid {
			return &s.repls[i]
		}
	}
	s.repls = append(s.repls, replNode{replid: replid})
	return &s.repls[len(s.repls)-1]
}

// Append adds an EID by its replica id and 48-bit GC value. No-op error on a
// packed set.
func (s *IDSet) Append(e mapi.EID) bool {
	return s.AppendRange(e.ReplID(), e.GCValue(), e.GCValue())
}

// AppendRange adds the inclusive GC-value range [lo,hi] under replid. Returns
// false on a packed set or an inverted range.
func (s *IDSet) AppendRange(replid uint16, lo, hi uint64) bool {
	if s.form.packed() || lo > hi {
		return false
	}
	s.findOrCreate(replid).ranges.insert(lo, hi)
	return true
}

// Remove drops a single EID. No-op on a packed set or an absent id.
func (s *IDSet) Remove(e mapi.EID) {
	if s.form.packed() {
		return
	}
	for i := range s.repls {
		if s.repls[i].replid == e.ReplID() {
			s.repls[i].ranges.erase(e.GCValue())
			return
		}
	}
}

// Contains reports whether the EID is in the set. A GUID-packed set is not
// queryable and always returns false (Convert it first).
func (s *IDSet) Contains(e mapi.EID) bool {
	if s.form == FormGUIDPacked {
		return false
	}
	for i := range s.repls {
		if s.repls[i].replid == e.ReplID() {
			return s.repls[i].ranges.contains(e.GCValue())
		}
	}
	return false
}

// Concatenate unions src into s (the cumulative upload merge). Both sets must be
// loose; returns false otherwise.
func (s *IDSet) Concatenate(src *IDSet) bool {
	if s.form.packed() || src.form.packed() {
		return false
	}
	for i := range src.repls {
		nd := &src.repls[i]
		for _, r := range nd.ranges.nodes {
			if !s.AppendRange(nd.replid, r.lo, r.hi) {
				return false
			}
		}
	}
	return true
}

// ForEachRange invokes fn for every [lo,hi] range in every replica, in stored
// order. The delta engine uses this to enumerate the client's given set
// (including foreign replicas, replid > 1) when computing deletions.
func (s *IDSet) ForEachRange(fn func(replid uint16, lo, hi uint64)) {
	for i := range s.repls {
		for _, r := range s.repls[i].ranges.nodes {
			fn(s.repls[i].replid, r.lo, r.hi)
		}
	}
}

// Convert turns a packed (just-deserialized) set into its loose, queryable
// form. id_packed flips to id_loose (the ranges are already replid-keyed);
// guid_packed resolves each block's GUID to a replid via the mapper and flips to
// guid_loose. Returns false if the set is already loose, or if a GUID block has
// no mapping.
func (s *IDSet) Convert() bool {
	switch s.form {
	case FormIDPacked:
		s.form = FormIDLoose
		return true
	case FormGUIDPacked:
		if s.mapper == nil {
			return false
		}
		for i := range s.repls {
			replid, ok := s.mapper.ToID(s.repls[i].replguid)
			if !ok {
				return false
			}
			s.repls[i].replid = replid
		}
		s.form = FormGUIDLoose
		return true
	default:
		return false
	}
}

// Serialize encodes a loose set as the wire IDSET: per non-empty replica, the
// replica key (replid as LE u16 for id_loose, or the 16-byte GUID for
// guid_loose) followed by its GLOBSET. The replica blocks are concatenated with
// no outer count (the buffer end terminates the set).
func (s *IDSet) Serialize() ([]byte, error) {
	out := make([]byte, 0, 64)
	for i := range s.repls {
		nd := &s.repls[i]
		if nd.ranges.empty() {
			continue
		}
		switch s.form {
		case FormIDLoose:
			var b [2]byte
			binary.LittleEndian.PutUint16(b[:], nd.replid)
			out = append(out, b[:]...)
		case FormGUIDLoose:
			if s.mapper == nil {
				return nil, fmt.Errorf("ics: guid_loose serialize requires a replica mapper")
			}
			guid, ok := s.mapper.ToGUID(nd.replid)
			if !ok {
				return nil, fmt.Errorf("ics: no GUID mapping for replid %d", nd.replid)
			}
			f := guid.Flat()
			out = append(out, f[:]...)
		default:
			return nil, fmt.Errorf("ics: cannot serialize packed form %#x", uint8(s.form))
		}
		out = append(out, serializeGlobset(nd.ranges)...)
	}
	return out, nil
}

// Deserialize parses the wire IDSET into a packed set (the form passed to
// NewIDSet selects replid vs GUID blocks). Call Convert to query or merge the
// result.
func (s *IDSet) Deserialize(data []byte) error {
	off := 0
	for off < len(data) {
		var nd replNode
		switch s.form {
		case FormIDPacked:
			if off+2 > len(data) {
				return fmt.Errorf("ics: truncated replid block")
			}
			nd.replid = binary.LittleEndian.Uint16(data[off:])
			off += 2
		case FormGUIDPacked:
			if off+16 > len(data) {
				return fmt.Errorf("ics: truncated replguid block")
			}
			var f mapi.FlatUID
			copy(f[:], data[off:off+16])
			nd.replguid = f.GUID()
			off += 16
		default:
			return fmt.Errorf("ics: cannot deserialize loose form %#x", uint8(s.form))
		}
		rs, consumed := deserializeGlobset(data[off:])
		if consumed == 0 {
			return fmt.Errorf("ics: empty GLOBSET block at offset %d", off)
		}
		off += consumed
		nd.ranges = rs
		s.repls = append(s.repls, nd)
	}
	return nil
}
