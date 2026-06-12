package ext

import "errors"

// Flags select context-dependent encodings. They mirror the EXT_FLAG_* set the
// reference server keys off, so a single primitive layer can produce both the
// internal (UTF-8) and the Exchange-wire (UTF-16) forms.
type Flags uint32

const (
	// FlagUTF16 encodes PtUnicode strings as UTF-16LE (the Exchange/NSP wire
	// form). When clear, Unicode strings are UTF-8, matching internal RPC.
	FlagUTF16 Flags = 1 << 0
	// FlagWCount widens the length prefix of a generic binary from 16 to 32
	// bits (and, later, restriction AND/OR child counts).
	FlagWCount Flags = 1 << 1
	// FlagTBLLMT limits packed-representation strings to a fixed length, used by
	// the contents/hierarchy table fetch paths. Its full effect lands with the
	// address-book/table serialization unit.
	FlagTBLLMT Flags = 1 << 2
	// FlagABK selects the address-book (MH-NSP) serialization mode, which gates
	// the value-present prefix on strings/binaries/multivalues and the
	// flagged-property-value type handling. Its full effect lands with the
	// address-book serialization unit.
	FlagABK Flags = 1 << 3
)

// Errors returned by the pull/push primitives. They replace the reference
// implementation's pack_result channel.
var (
	// ErrUnderflow is returned when a read would pass the end of the buffer.
	ErrUnderflow = errors.New("ext: buffer underflow")
	// ErrFormat is returned when bytes are malformed for the requested type
	// (e.g. a 16-bit length prefix that cannot hold the value, an unterminated
	// string, or an unsupported property type).
	ErrFormat = errors.New("ext: malformed data")
)

// Push appends little-endian, unaligned bytes to a growable buffer.
type Push struct {
	buf   []byte
	flags Flags
}

// NewPush returns a Push using the given encoding flags.
func NewPush(flags Flags) *Push { return &Push{flags: flags} }

// Bytes returns the bytes accumulated so far. The slice aliases the internal
// buffer; copy it if it must outlive further writes.
func (p *Push) Bytes() []byte { return p.buf }

// Len reports how many bytes have been written.
func (p *Push) Len() int { return len(p.buf) }

// Pull reads little-endian, unaligned bytes from a fixed buffer.
type Pull struct {
	buf   []byte
	off   int
	flags Flags
}

// NewPull returns a Pull over buf using the given encoding flags.
func NewPull(buf []byte, flags Flags) *Pull { return &Pull{buf: buf, flags: flags} }

// Offset reports the current read position.
func (p *Pull) Offset() int { return p.off }

// Remaining reports how many unread bytes are left.
func (p *Pull) Remaining() int { return len(p.buf) - p.off }

// need verifies that n more bytes are available.
func (p *Pull) need(n int) error {
	if n < 0 || p.off+n > len(p.buf) {
		return ErrUnderflow
	}
	return nil
}
