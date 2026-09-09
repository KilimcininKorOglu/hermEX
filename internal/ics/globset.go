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
	glbBitmask = 0x42 // 'B', bitmask expansion (decode only; see deserializeGlobset)
	glbPop     = 0x50 // 'P', pop the top stack frame
	glbRange   = 0x52 // 'R', a low/high pair, each (6-depth) bytes
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
		out = appendNodeUnder(out, nd, stackLen)
	}
	if stackLen != 0 {
		out = append(out, glbPop)
	}
	return append(out, glbEnd)
}

// appendNodeUnder emits one range under the global prefix already pushed to
// stackLen bytes. A singleton pushes to depth 6, which makes the decoder
// auto-emit it and auto-pop the frame, so no explicit pop is needed; a wider
// range folds its own inner prefix before the range command.
func appendNodeUnder(out []byte, nd rangeNode, stackLen int) []byte {
	loGC := mapi.ValueToGC(nd.lo)
	if nd.hi == nd.lo {
		return pushCmd(out, 6-stackLen, loGC[stackLen:6])
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
	return out
}

// pushCmd writes a "push N" command: the length byte (1..6) then the N common
// bytes.
func pushCmd(out []byte, length int, common []byte) []byte {
	// #nosec G115 -- a deliberate little-endian split of the wider value
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
	d := globsetDecoder{data: data}
	for d.off < len(data) {
		cmd := data[d.off]
		d.off++
		if d.command(cmd) {
			return d.rs, d.off
		}
	}
	return d.rs, d.off
}

// globFrame is one pushed run of common bytes.
type globFrame struct {
	n     int
	bytes [6]byte
}

// globsetDecoder holds the decode state: the cursor, the common-byte stack, and
// the ranges read so far.
type globsetDecoder struct {
	data  []byte
	off   int
	stack []globFrame
	rs    rangeSet
}

// common assembles the GLOBCNT prefix the stack currently holds, and its length.
func (d *globsetDecoder) common() (mapi.GlobCnt, int) {
	var gc mapi.GlobCnt
	t := 0
	for _, f := range d.stack {
		copy(gc[t:], f.bytes[:f.n])
		t += f.n
	}
	return gc, t
}

// command runs one GLOBSET command, reporting whether the stream ends here:
// either the end command, or a truncated/malformed one, which stops at the
// current offset rather than panicking.
func (d *globsetDecoder) command(cmd byte) (stop bool) {
	switch {
	case cmd >= 0x01 && cmd <= 0x06:
		return d.push(int(cmd))
	case cmd == glbBitmask:
		return d.bitmask()
	case cmd == glbPop:
		d.pop()
		return false
	case cmd == glbRange:
		return d.readRange()
	}
	return true // glbEnd, and anything unrecognized
}

// push reads a run of n common bytes. At depth 6 the frame names one value, so
// the decoder emits it and pops the frame at once.
func (d *globsetDecoder) push(n int) (stop bool) {
	if d.off+n > len(d.data) {
		return true
	}
	_, cur := d.common()
	if cur+n > 6 {
		return true
	}
	var f globFrame
	f.n = n
	copy(f.bytes[:], d.data[d.off:d.off+n])
	d.off += n
	d.stack = append(d.stack, f)
	if cur+n == 6 {
		gc, _ := d.common()
		x := mapi.GCToValue(gc)
		d.rs.appendRaw(x, x)
		d.pop()
	}
	return false
}

// pop drops the innermost common-byte frame.
func (d *globsetDecoder) pop() {
	if len(d.stack) > 0 {
		d.stack = d.stack[:len(d.stack)-1]
	}
}

// bitmask reads the decode-only bitmask command: a base low byte plus eight bits
// of successors.
func (d *globsetDecoder) bitmask() (stop bool) {
	gc, cur := d.common()
	if cur != 5 || d.off+2 > len(d.data) {
		return true
	}
	gc[5] = d.data[d.off]
	mask := d.data[d.off+1]
	d.off += 2
	d.rs.appendBitmask(mapi.GCToValue(gc), mask)
	return false
}

// readRange reads a range command: the low tail then the high tail, each filling
// the bytes the common prefix left.
func (d *globsetDecoder) readRange() (stop bool) {
	gc, cur := d.common()
	if cur > 6 {
		return true
	}
	cnt := 6 - cur
	if d.off+2*cnt > len(d.data) {
		return true
	}
	loGC, hiGC := gc, gc
	copy(loGC[cur:], d.data[d.off:d.off+cnt])
	d.off += cnt
	copy(hiGC[cur:], d.data[d.off:d.off+cnt])
	d.off += cnt
	d.rs.appendRaw(mapi.GCToValue(loGC), mapi.GCToValue(hiGC))
	return false
}

// appendBitmask appends the ranges one bitmask byte encodes. The base value is
// always present; bit i (0..7) represents low+i+1. A set bit extends the pending
// range; a clear bit flushes it.
func (rs *rangeSet) appendBitmask(low uint64, mask byte) {
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
			continue
		}
		v := low + uint64(i) + 1
		pendLo, pendHi = v, v
		pendActive = true
	}
	if pendActive {
		rs.appendRaw(pendLo, pendHi)
	}
}
