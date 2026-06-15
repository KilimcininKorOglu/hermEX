package ics

// Parser reassembles a FastTransfer element stream from transport chunks
// (PutBuffer fragments) that may split an element at ANY byte boundary,
// including mid-primitive. Feed appends a chunk; Next yields each complete
// element in order and reports NeedMore (ok=false) when the buffered bytes hold
// only part of the next element — the caller then Feeds more and retries. This
// length-driven reassembly is what makes the reader interoperate with a real
// client's arbitrary chunking, which we cannot otherwise observe.
type Parser struct {
	buf []byte
	pos int
}

// Feed appends a transport chunk, first discarding the already-consumed prefix
// so the buffer stays bounded.
func (ps *Parser) Feed(chunk []byte) {
	if ps.pos > 0 {
		ps.buf = append(ps.buf[:0], ps.buf[ps.pos:]...)
		ps.pos = 0
	}
	ps.buf = append(ps.buf, chunk...)
}

// Next returns the next complete element. ok is false when more bytes are needed
// (the partial element is retained; call Feed then Next again). err is set only
// for a malformed or unsupported element.
func (ps *Parser) Next() (it Item, ok bool, err error) {
	item, consumed, complete, err := decodeElement(ps.buf[ps.pos:])
	if err != nil {
		return Item{}, false, err
	}
	if !complete {
		return Item{}, false, nil
	}
	ps.pos += consumed
	return item, true, nil
}
