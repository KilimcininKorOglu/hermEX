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
	return r.element(&page, 0)
}

// element parses one element: any leading SWITCH_PAGE, the tag byte, and, when
// the content bit is set, the content items up to the matching END. A nested
// element on a different page is introduced by its own SWITCH_PAGE, handled by
// the recursive call.
//
// depth is the element's nesting level, checked because the descent is recursive
// and a stack overflow is fatal to the process rather than to the request.
func (r *reader) element(page *byte, depth int) (*Node, error) {
	if depth > maxNestingDepth {
		return nil, ErrTooDeep
	}
	if err := r.skipSwitchPages(page); err != nil {
		return nil, err
	}
	n, hasContent, err := r.startTag(*page)
	if err != nil || !hasContent {
		return n, err
	}
	return r.readContent(n, page, depth)
}

// skipSwitchPages consumes the leading SWITCH_PAGE tokens, leaving page on the last
// code page they name.
func (r *reader) skipSwitchPages(page *byte) error {
	for {
		b, err := r.peek()
		if err != nil {
			return err
		}
		if b != gSwitchPage {
			return nil
		}
		r.off++
		p, err := r.readByte()
		if err != nil {
			return err
		}
		*page = p
	}
}

// startTag reads the element's tag byte, rejecting the tokens that cannot open an
// element, and reports whether the element carries content.
func (r *reader) startTag(page byte) (*Node, bool, error) {
	tok, err := r.readByte()
	if err != nil {
		return nil, false, err
	}
	if tok&cbAttributes != 0 {
		return nil, false, ErrFormat
	}
	if tok == gEnd || tok == gStrI || tok == gOpaque {
		return nil, false, ErrFormat
	}
	return &Node{Tag: Tag(uint16(page)<<8 | uint16(tok&tokenMask))}, tok&cbContent != 0, nil
}

// readContent reads the element's content items up to the matching END: inline
// strings, opaque data, and nested elements.
func (r *reader) readContent(n *Node, page *byte, depth int) (*Node, error) {
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
			err = r.readString(n)
		case gOpaque:
			r.off++
			err = r.readOpaque(n)
		default:
			err = r.readChild(n, page, depth)
		}
		if err != nil {
			return nil, err
		}
	}
}

// readString appends one inline string to the element's text.
func (r *reader) readString(n *Node) error {
	s, err := r.cstr()
	if err != nil {
		return err
	}
	n.Text += s
	return nil
}

// readOpaque appends one length-prefixed opaque item to the element.
func (r *reader) readOpaque(n *Node) error {
	l, err := r.mbUint()
	if err != nil {
		return err
	}
	data, err := r.take(int(l))
	if err != nil {
		return err
	}
	n.Opaque = append(n.Opaque, data...)
	return nil
}

// readChild parses one nested element and appends it.
func (r *reader) readChild(n *Node, page *byte, depth int) error {
	child, err := r.element(page, depth+1)
	if err != nil {
		return err
	}
	n.Children = append(n.Children, child)
	return nil
}
