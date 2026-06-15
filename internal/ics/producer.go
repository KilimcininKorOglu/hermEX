package ics

import (
	"encoding/binary"

	"hermex/internal/mapi"
)

// element is one stream element split into an atomic header (never torn across a
// chunk boundary) and a tearable body (the raw payload of a large string or
// binary value).
type element struct {
	header     []byte
	body       []byte
	headerDone bool
	bodyPos    int
}

// Producer accumulates FastTransfer elements (markers + properties) and serves
// them as transport chunks via ReadBuffer. A property's variable-length payload
// may be split across chunks; a marker, propdef, fixed value, or length prefix
// is never split, so a conforming reader needs no special handling for our
// chunk boundaries. Writing and draining interleave: a caller may append more
// elements between ReadBuffer calls (the download flow feeds one message at a
// time and drains in between).
type Producer struct {
	queue []element
	head  int // index of the first not-fully-drained element
}

// WriteMarker appends a structural marker.
func (pr *Producer) WriteMarker(m uint32) {
	pr.queue = append(pr.queue, element{header: binary.LittleEndian.AppendUint32(nil, m)})
}

// WriteProp appends a property value. PT_SVREID is silently dropped — it has no
// FastTransfer form and never appears in a valid stream. Any other unsupported
// type returns an error so the caller can exclude the property rather than
// corrupt the stream.
func (pr *Producer) WriteProp(p StreamProp) error {
	if p.Tag.Type() == mapi.PtSvrEID {
		return nil
	}
	header, body, err := encodeProp(p)
	if err != nil {
		return err
	}
	pr.queue = append(pr.queue, element{header: header, body: body})
	return nil
}

// Pending reports whether any bytes remain to be drained.
func (pr *Producer) Pending() bool { return pr.head < len(pr.queue) }

// ReadBuffer serves up to maxLen bytes of the pending stream. It emits whole
// elements, splitting only inside a large value's body when a single element
// exceeds maxLen. last reports the stream is fully drained, after which the
// producer resets for reuse. A single atomic header larger than maxLen (a rare
// huge multivalue) is emitted as one oversized chunk — large multivalues are a
// documented v1 limitation; large strings/binaries are torn safely.
func (pr *Producer) ReadBuffer(maxLen int) (chunk []byte, last bool) {
	var out []byte
	for pr.head < len(pr.queue) {
		e := &pr.queue[pr.head]
		if !e.headerDone {
			if len(out) > 0 && len(out)+len(e.header) > maxLen {
				break // defer this element to the next chunk
			}
			out = append(out, e.header...)
			e.headerDone = true
		}
		avail := max(maxLen-len(out), 0) // 0 if the header alone overflowed maxLen
		rem := len(e.body) - e.bodyPos
		if rem <= avail {
			out = append(out, e.body[e.bodyPos:]...)
			pr.head++
			continue
		}
		out = append(out, e.body[e.bodyPos:e.bodyPos+avail]...)
		e.bodyPos += avail
		break
	}
	last = pr.head >= len(pr.queue)
	if last {
		pr.queue = pr.queue[:0]
		pr.head = 0
	}
	return out, last
}
