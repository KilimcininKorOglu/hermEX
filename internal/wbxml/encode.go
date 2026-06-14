package wbxml

// writer appends WBXML bytes to a growable buffer.
type writer struct {
	buf []byte
}

// mbUint appends v as a WBXML multi-byte integer: big-endian, seven bits per
// byte, with the continuation bit (0x80) set on every byte but the last.
func (w *writer) mbUint(v uint32) {
	var stack [5]byte
	n := 0
	stack[n] = byte(v & 0x7F)
	n++
	for v >>= 7; v > 0; v >>= 7 {
		stack[n] = byte(v&0x7F) | 0x80
		n++
	}
	for i := n - 1; i >= 0; i-- {
		w.buf = append(w.buf, stack[i])
	}
}

// strI appends an STR_I inline string: the UTF-8 bytes (with embedded NULs
// stripped, since NUL terminates the string) followed by the NUL terminator.
func (w *writer) strI(s string) {
	w.buf = append(w.buf, gStrI)
	for i := 0; i < len(s); i++ {
		if s[i] != 0 {
			w.buf = append(w.buf, s[i])
		}
	}
	w.buf = append(w.buf, 0x00)
}

// Marshal encodes a WBXML document: the fixed four-field ActiveSync header
// followed by the element tree rooted at root. A nil root yields just the
// header. The initial code page is AirSync (0); SWITCH_PAGE is emitted lazily
// whenever an element's page differs from the current one.
func Marshal(root *Node) []byte {
	w := &writer{}
	w.buf = append(w.buf, version)
	w.mbUint(publicID)
	w.mbUint(charsetUTF8)
	w.mbUint(stringTableLen)
	if root != nil {
		page := byte(PageAirSync)
		w.element(root, &page)
	}
	return w.buf
}

// element encodes one node and its content, switching the code page first when
// the node lives on a different page than the current one.
func (w *writer) element(n *Node, page *byte) {
	if p := n.Tag.Page(); p != *page {
		w.buf = append(w.buf, gSwitchPage, p)
		*page = p
	}
	tok := n.Tag.Token()
	switch {
	case len(n.Children) > 0:
		w.buf = append(w.buf, tok|cbContent)
		for _, c := range n.Children {
			w.element(c, page)
		}
		w.buf = append(w.buf, gEnd)
	case n.Opaque != nil:
		w.buf = append(w.buf, tok|cbContent, gOpaque)
		w.mbUint(uint32(len(n.Opaque)))
		w.buf = append(w.buf, n.Opaque...)
		w.buf = append(w.buf, gEnd)
	case n.Text != "":
		w.buf = append(w.buf, tok|cbContent)
		w.strI(n.Text)
		w.buf = append(w.buf, gEnd)
	default:
		w.buf = append(w.buf, tok)
	}
}
