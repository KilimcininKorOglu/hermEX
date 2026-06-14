package wbxml

import "errors"

// Errors returned by the codec.
var (
	// ErrUnderflow is returned when a read would pass the end of the buffer.
	ErrUnderflow = errors.New("wbxml: buffer underflow")
	// ErrFormat is returned when bytes are malformed: a bad header, a tag that
	// carries attributes (unused in ActiveSync), a global token where an element
	// was expected, or a multi-byte integer longer than five bytes.
	ErrFormat = errors.New("wbxml: malformed data")
)

// WBXML header fields. ActiveSync fixes all four and rejects any deviation:
// WBXML 1.3, public identifier 1, charset 106 (UTF-8), and an empty string
// table.
const (
	version        byte   = 0x03
	publicID       uint32 = 0x01
	charsetUTF8    uint32 = 106
	stringTableLen uint32 = 0
)

// Global tokens and the tag control bits (WBXML 1.3 / MS-ASWBXML).
const (
	gSwitchPage byte = 0x00
	gEnd        byte = 0x01
	gStrI       byte = 0x03
	gOpaque     byte = 0xC3

	cbContent    byte = 0x40 // the tag has element content
	cbAttributes byte = 0x80 // the tag has attributes — rejected
	tokenMask    byte = 0x3F // the tag token, masking the control bits
)

// Node is one element of a WBXML document. A node carries exactly one form of
// content: child elements (Children), an inline string (Text), opaque bytes
// (Opaque), or none (a self-closing element). When more than one is set, the
// encoder prefers Children, then Opaque, then Text.
type Node struct {
	Tag      Tag
	Children []*Node
	Text     string
	Opaque   []byte
}

// Elem builds a container element holding the given children.
func Elem(tag Tag, children ...*Node) *Node { return &Node{Tag: tag, Children: children} }

// Str builds an element whose content is an inline string.
func Str(tag Tag, s string) *Node { return &Node{Tag: tag, Text: s} }

// Opaque builds an element whose content is opaque (length-prefixed) bytes,
// the binary-safe framing ActiveSync uses for message bodies.
func Opaque(tag Tag, b []byte) *Node { return &Node{Tag: tag, Opaque: b} }

// Empty builds a self-closing element with no content.
func Empty(tag Tag) *Node { return &Node{Tag: tag} }

// Child returns the first direct child with the given tag, or nil.
func (n *Node) Child(tag Tag) *Node {
	for _, c := range n.Children {
		if c.Tag == tag {
			return c
		}
	}
	return nil
}

// ChildText returns the inline text of the first direct child with the given
// tag, or "" when there is no such child.
func (n *Node) ChildText(tag Tag) string {
	if c := n.Child(tag); c != nil {
		return c.Text
	}
	return ""
}
