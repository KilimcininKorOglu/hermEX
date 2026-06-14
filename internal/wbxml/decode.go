package wbxml

// reader reads WBXML bytes from a fixed buffer with a forward cursor.
type reader struct {
	buf []byte
	off int
}

// need verifies that n more bytes are available.
func (r *reader) need(n int) error {
	if n < 0 || r.off+n > len(r.buf) {
		return ErrUnderflow
	}
	return nil
}

// readByte consumes and returns the next byte.
func (r *reader) readByte() (byte, error) {
	if err := r.need(1); err != nil {
		return 0, err
	}
	b := r.buf[r.off]
	r.off++
	return b, nil
}

// peek returns the next byte without consuming it.
func (r *reader) peek() (byte, error) {
	if err := r.need(1); err != nil {
		return 0, err
	}
	return r.buf[r.off], nil
}

// mbUint reads a WBXML multi-byte integer, rejecting any encoding longer than
// five bytes (which cannot fit a 32-bit value).
func (r *reader) mbUint() (uint32, error) {
	var v uint32
	for range 5 {
		b, err := r.readByte()
		if err != nil {
			return 0, err
		}
		v = v<<7 | uint32(b&0x7F)
		if b&0x80 == 0 {
			return v, nil
		}
	}
	return 0, ErrFormat
}

// cstr reads a NUL-terminated string. A missing terminator is an underflow.
func (r *reader) cstr() (string, error) {
	for i := r.off; i < len(r.buf); i++ {
		if r.buf[i] == 0 {
			s := string(r.buf[r.off:i])
			r.off = i + 1
			return s, nil
		}
	}
	return "", ErrUnderflow
}

// take consumes and returns the next n bytes as a fresh copy.
func (r *reader) take(n int) ([]byte, error) {
	if err := r.need(n); err != nil {
		return nil, err
	}
	out := make([]byte, n)
	copy(out, r.buf[r.off:r.off+n])
	r.off += n
	return out, nil
}

// Unmarshal decodes a WBXML document into its element tree. It validates the
// fixed ActiveSync header, then parses the single root element; any bytes after
// the root are ignored. Tags carrying attributes and unsupported global tokens
// are rejected.
func Unmarshal(b []byte) (*Node, error) {
	r := &reader{buf: b}
	v, err := r.readByte()
	if err != nil {
		return nil, err
	}
	if v != version {
		return nil, ErrFormat
	}
	for _, want := range []uint32{publicID, charsetUTF8, stringTableLen} {
		got, err := r.mbUint()
		if err != nil {
			return nil, err
		}
		if got != want {
			return nil, ErrFormat
		}
	}
	page := byte(PageAirSync)
	return r.element(&page)
}

// element parses one element: any leading SWITCH_PAGE, the tag byte, and — when
// the content bit is set — the content items up to the matching END. A nested
// element on a different page is introduced by its own SWITCH_PAGE, handled by
// the recursive call.
func (r *reader) element(page *byte) (*Node, error) {
	for {
		b, err := r.peek()
		if err != nil {
			return nil, err
		}
		if b != gSwitchPage {
			break
		}
		r.off++
		p, err := r.readByte()
		if err != nil {
			return nil, err
		}
		*page = p
	}

	tok, err := r.readByte()
	if err != nil {
		return nil, err
	}
	if tok&cbAttributes != 0 {
		return nil, ErrFormat
	}
	if tok == gEnd || tok == gStrI || tok == gOpaque {
		return nil, ErrFormat
	}
	n := &Node{Tag: Tag(uint16(*page)<<8 | uint16(tok&tokenMask))}
	if tok&cbContent == 0 {
		return n, nil
	}

	for {
		b, err := r.peek()
		if err != nil {
			return nil, err
		}
		switch b {
		case gEnd:
			r.off++
			return n, nil
		case gStrI:
			r.off++
			s, err := r.cstr()
			if err != nil {
				return nil, err
			}
			n.Text += s
		case gOpaque:
			r.off++
			l, err := r.mbUint()
			if err != nil {
				return nil, err
			}
			data, err := r.take(int(l))
			if err != nil {
				return nil, err
			}
			n.Opaque = append(n.Opaque, data...)
		default:
			child, err := r.element(page)
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, child)
		}
	}
}
