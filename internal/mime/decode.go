package mime

import (
	"bytes"
	"encoding/base64"
	"io"
	stdmime "mime"
	"mime/quotedprintable"
	"strings"
)

// DecodedContent returns the part's body with its Content-Transfer-Encoding
// removed (base64 or quoted-printable decoded; 7bit/8bit/binary returned as-is).
// It is for display and download; the wire-facing section extraction in
// Extract deliberately leaves the encoding intact.
func (p *Part) DecodedContent() ([]byte, error) {
	body := p.raw[p.bodyOffset:]
	switch strings.ToLower(strings.TrimSpace(p.Encoding)) {
	case "base64":
		clean := stripASCIISpace(body)
		out := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
		n, err := base64.StdEncoding.Decode(out, clean)
		return out[:n], err
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
	default:
		return body, nil
	}
}

// Filename returns the part's suggested file name from its Content-Disposition
// filename parameter, falling back to the Content-Type name parameter.
func (p *Part) Filename() string {
	if p.DispParams != nil {
		if fn := p.DispParams["filename"]; fn != "" {
			return decodeMaybeWord(fn)
		}
	}
	if p.Params != nil {
		if n := p.Params["name"]; n != "" {
			return decodeMaybeWord(n)
		}
	}
	return ""
}

// decodeMaybeWord decodes an RFC 2047 encoded-word if present.
func decodeMaybeWord(s string) string {
	if d, err := new(stdmime.WordDecoder).DecodeHeader(s); err == nil {
		return d
	}
	return s
}

// stripASCIISpace removes ASCII whitespace (the line breaks base64 bodies carry)
// so the result is a contiguous base64 string.
func stripASCIISpace(b []byte) []byte {
	out := b[:0:0]
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
		default:
			out = append(out, c)
		}
	}
	return out
}
