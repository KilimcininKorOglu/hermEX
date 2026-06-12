package imap

import (
	"fmt"
	"strconv"
	"strings"
)

// seqStar is the wire token '*', meaning the largest value in use (the highest
// message sequence number, or the highest UID for a UID command). It is
// resolved against the mailbox at evaluation time.
const seqStar = ^uint32(0)

// seqRange is one inclusive range of a sequence set. lo and hi are stored as
// written (either bound may be seqStar); contains normalizes them.
type seqRange struct {
	lo, hi uint32
}

// seqSet is an IMAP sequence set: a comma-separated union of numbers and
// ranges, e.g. "1", "2:4", "*", "1,3,5:*".
type seqSet []seqRange

// parseSeqSet parses a sequence-set token. It rejects an empty set and zero,
// which is never a valid message number or UID.
func parseSeqSet(s string) (seqSet, error) {
	if s == "" {
		return nil, fmt.Errorf("%w: empty sequence set", errProtocol)
	}
	var ss seqSet
	for part := range strings.SplitSeq(s, ",") {
		lo, hi, found := strings.Cut(part, ":")
		a, err := parseSeqNum(lo)
		if err != nil {
			return nil, err
		}
		if !found {
			ss = append(ss, seqRange{lo: a, hi: a})
			continue
		}
		b, err := parseSeqNum(hi)
		if err != nil {
			return nil, err
		}
		ss = append(ss, seqRange{lo: a, hi: b})
	}
	return ss, nil
}

// parseSeqNum parses one bound of a sequence set: a positive integer or '*'.
func parseSeqNum(s string) (uint32, error) {
	if s == "*" {
		return seqStar, nil
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("%w: bad sequence number %q", errProtocol, s)
	}
	return uint32(n), nil
}

// contains reports whether n falls in the set, resolving '*' to max (the
// largest value currently in use). Ranges are order-independent per RFC 3501,
// so a:b and b:a denote the same range.
func (ss seqSet) contains(n, max uint32) bool {
	for _, r := range ss {
		lo, hi := r.resolve(max)
		if lo > hi {
			lo, hi = hi, lo
		}
		if n >= lo && n <= hi {
			return true
		}
	}
	return false
}

// resolve substitutes max for any '*' bound.
func (r seqRange) resolve(max uint32) (uint32, uint32) {
	lo, hi := r.lo, r.hi
	if lo == seqStar {
		lo = max
	}
	if hi == seqStar {
		hi = max
	}
	return lo, hi
}
