package mime

import (
	"bytes"
	"encoding/base64"
	"io"
	stdmime "mime"
	"mime/quotedprintable"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"
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

// DecodedText returns the part's body as a UTF-8 string: its transfer encoding
// removed (DecodedContent) and its declared charset converted to UTF-8
// (DecodeCharset). It is the text form used for PR_BODY and for display.
func (p *Part) DecodedText() (string, error) {
	body, err := p.DecodedContent()
	if err != nil {
		return "", err
	}
	return DecodeCharset(body, p.Params["charset"]), nil
}

// DecodeCharset converts bytes in the named charset to a UTF-8 string. UTF-8 and
// US-ASCII pass through unchanged; anything else is transcoded with a real
// charset table.
//
// The decoded string is all that survives: the plain-text body is stored as
// PR_BODY and the original bytes are not kept anywhere, so reading them as UTF-8
// because the name was unfamiliar corrupts the message permanently, and every
// later reader (IMAP, EWS, ActiveSync, the web UI) sees only the damage. That
// makes broad charset coverage a correctness requirement, not a nicety: Cyrillic,
// Greek, Hebrew, Arabic, Japanese, Korean and Chinese mail all routinely declare
// charsets outside the Western European set.
//
// A name nothing recognizes, or a byte sequence the table rejects, still falls
// back to the raw string. Losing the body entirely would be worse than showing it
// imperfectly.
func DecodeCharset(b []byte, charset string) string {
	name := strings.ToLower(strings.TrimSpace(charset))
	switch name {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return string(b)
	}
	enc, err := htmlindex.Get(name)
	if err != nil || enc == nil {
		// htmlindex covers the WHATWG set and its aliases; the IANA registry
		// carries names that set omits (and mail does use them).
		if enc, err = ianaindex.MIME.Encoding(name); err != nil || enc == nil {
			return string(b)
		}
	}
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(out)
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
