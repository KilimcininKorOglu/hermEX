package ics

import (
	"sort"

	"hermex/internal/mapi"
)

// GLOBSET command bytes ([MS-OXCFXICS] 2.2.2.6). Values 0x01..0x06 are the
// "push N common bytes" commands (the byte itself is the byte count); the four
// below are the fixed commands.
const (
	glbEnd     = 0x00 // terminates a replica's GLOBSET
	glbBitmask = 0x42 // 'B' — bitmask expansion (decode only; see deserializeGlobset)
	glbPop     = 0x50 // 'P' — pop the top stack frame
	glbRange   = 0x52 // 'R' — a low/high pair, each (6-depth) bytes
)

// rangeNode is one inclusive [lo,hi] interval of 48-bit global-counter values.
type rangeNode struct {
	lo, hi uint64
}

// rangeSet is a replica's set of GC-value ranges (the GLOBSET payload). After
// insert it is sorted, disjoint, and non-adjacent (ranges separated by a gap of
// at most one value are coalesced). Decode appends ranges verbatim via appendRaw
// to keep decode->encode faithful, so a decoded set is ordered by the wire, not
// necessarily coalesced.
type rangeSet struct {
	nodes []rangeNode
}

func (rs *rangeSet) empty() bool { return len(rs.nodes) == 0 }

// insert adds [lo,hi] and coalesces. Two ranges merge when the gap between them
// is at most one value (lo <= prev.hi+1), the GLOBSET coalescing invariant. GC
// values are 48-bit so hi+1 never overflows.
func (rs *rangeSet) insert(lo, hi uint64) {
	rs.nodes = append(rs.nodes, rangeNode{lo, hi})
	sort.Slice(rs.nodes, func(i, j int) bool { return rs.nodes[i].lo < rs.nodes[j].lo })
	out := rs.nodes[:0]
	for _, nd := range rs.nodes {
		if len(out) > 0 {
			last := &out[len(out)-1]
			if nd.lo <= last.hi+1 {
				if nd.hi > last.hi {
					last.hi = nd.hi
				}
				continue
			}
		}
		out = append(out, nd)
	}
	rs.nodes = out
}

// appendRaw adds [lo,hi] without coalescing, preserving wire order on decode.
func (rs *rangeSet) appendRaw(lo, hi uint64) {
	rs.nodes = append(rs.nodes, rangeNode{lo, hi})
}

// erase removes a single value, splitting the containing range if needed.
func (rs *rangeSet) erase(v uint64) {
	var out []rangeNode
	for _, nd := range rs.nodes {
		if v < nd.lo || v > nd.hi {
			out = append(out, nd)
			continue
		}
		if v > nd.lo {
			out = append(out, rangeNode{nd.lo, v - 1})
		}
		if v < nd.hi {
			out = append(out, rangeNode{v + 1, nd.hi})
		}
	}
	rs.nodes = out
}

// contains reports whether v lies in any range (scans; works on coalesced or
// raw-ordered sets).
func (rs *rangeSet) contains(v uint64) bool {
	for _, nd := range rs.nodes {
		if v >= nd.lo && v <= nd.hi {
			return true
		}
	}
	return false
}

// commonPrefixLen counts the leading bytes shared by two big-endian GlobCnts
// (0..6). The prefix is the most-significant common bytes folded onto the stack.
func commonPrefixLen(a, b mapi.GlobCnt) int {
	n := 0
	for n < 6 && a[n] == b[n] {
		n++
	}
	return n
}

// serializeGlobset encodes a replica's ranges as the GLOBSET command stream
// ([MS-OXCFXICS] 2.2.2.6.1). It folds a global common prefix across the whole
// set, then a per-range inner prefix, carrying
// only the differing low-order bytes inline. It never emits the bitmask command
// (a valid encoder choice; the decoder still accepts it).
func serializeGlobset(rs rangeSet) []byte {
	out := make([]byte, 0, 16)
	nodes := rs.nodes
	switch len(nodes) {
	case 0:
		return append(out, glbEnd)
	case 1:
		loGC := mapi.ValueToGC(nodes[0].lo)
		if nodes[0].hi == nodes[0].lo {
			out = pushCmd(out, 6, loGC[:6])
		} else {
			hiGC := mapi.ValueToGC(nodes[0].hi)
			out = rangeCmd(out, loGC[:6], hiGC[:6])
		}
		return append(out, glbEnd)
	}
	frontGC := mapi.ValueToGC(nodes[0].lo)
	backGC := mapi.ValueToGC(nodes[len(nodes)-1].hi)
	stackLen := commonPrefixLen(frontGC, backGC)
	if stackLen != 0 {
		out = pushCmd(out, stackLen, frontGC[:stackLen])
	}
	for _, nd := range nodes {
		loGC := mapi.ValueToGC(nd.lo)
		if nd.hi == nd.lo {
			// Pushing to depth 6 makes the decoder auto-emit this singleton
			// and auto-pop the frame, so no explicit pop is needed.
			out = pushCmd(out, 6-stackLen, loGC[stackLen:6])
			continue
		}
		hiGC := mapi.ValueToGC(nd.hi)
		i := stackLen
		for i < 6 && loGC[i] == hiGC[i] {
			i++
		}
		if i > stackLen {
			out = pushCmd(out, i-stackLen, loGC[stackLen:i])
		}
		out = rangeCmd(out, loGC[i:6], hiGC[i:6])
		if i > stackLen {
			out = append(out, glbPop)
		}
	}
	if stackLen != 0 {
		out = append(out, glbPop)
	}
	return append(out, glbEnd)
}

// pushCmd writes a "push N" command: the length byte (1..6) then the N common
// bytes.
func pushCmd(out []byte, length int, common []byte) []byte {
	out = append(out, byte(length))
	return append(out, common...)
}

// rangeCmd writes a "range" command: 0x52 then the low tail then the high tail
// (each len(lo) bytes).
func rangeCmd(out []byte, lo, hi []byte) []byte {
	out = append(out, glbRange)
	out = append(out, lo...)
	return append(out, hi...)
}

// deserializeGlobset decodes a GLOBSET command stream into a rangeSet and
// returns the number of bytes consumed (past the terminating end command). It
// maintains the common-byte stack and handles push (incl. the depth-6 auto-emit
// + auto-pop), range, pop, and the decode-only bitmask command. Ranges are
// appended in wire order without coalescing. A truncated or malformed stream
// stops at the current offset rather than panicking.
func deserializeGlobset(data []byte) (rangeSet, int) {
	var rs rangeSet
	type frame struct {
		n     int
		bytes [6]byte
	}
	var stack []frame
	common := func() (mapi.GlobCnt, int) {
		var gc mapi.GlobCnt
		t := 0
		for _, f := range stack {
			copy(gc[t:], f.bytes[:f.n])
			t += f.n
		}
		return gc, t
	}
	off := 0
	for off < len(data) {
		cmd := data[off]
		off++
		switch {
		case cmd == glbEnd:
			return rs, off
		case cmd >= 0x01 && cmd <= 0x06:
			n := int(cmd)
			if off+n > len(data) {
				return rs, off
			}
			_, cur := common()
			if cur+n > 6 {
				return rs, off
			}
			var f frame
			f.n = n
			copy(f.bytes[:], data[off:off+n])
			off += n
			stack = append(stack, f)
			if cur+n == 6 {
				gc, _ := common()
				x := mapi.GCToValue(gc)
				rs.appendRaw(x, x)
				stack = stack[:len(stack)-1]
			}
		case cmd == glbBitmask:
			gc, cur := common()
			if cur != 5 || off+2 > len(data) {
				return rs, off
			}
			start := data[off]
			mask := data[off+1]
			off += 2
			gc[5] = start
			low := mapi.GCToValue(gc)
			// The base value is always present; bit i (0..7) represents
			// low+i+1. A set bit extends the pending range; a clear bit flushes
			// it.
			pendActive := true
			pendLo, pendHi := low, low
			for i := range 8 {
				if mask&(1<<uint(i)) == 0 {
					if pendActive {
						rs.appendRaw(pendLo, pendHi)
						pendActive = false
					}
					continue
				}
				if pendActive {
					pendHi++
				} else {
					v := low + uint64(i) + 1
					pendLo, pendHi = v, v
					pendActive = true
				}
			}
			if pendActive {
				rs.appendRaw(pendLo, pendHi)
			}
		case cmd == glbPop:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case cmd == glbRange:
			gc, cur := common()
			if cur > 6 {
				return rs, off
			}
			cnt := 6 - cur
			if off+2*cnt > len(data) {
				return rs, off
			}
			loGC := gc
			hiGC := gc
			copy(loGC[cur:], data[off:off+cnt])
			off += cnt
			copy(hiGC[cur:], data[off:off+cnt])
			off += cnt
			rs.appendRaw(mapi.GCToValue(loGC), mapi.GCToValue(hiGC))
		default:
			return rs, off
		}
	}
	return rs, off
}
