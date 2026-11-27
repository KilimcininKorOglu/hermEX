package mime

import (
	"bytes"
	"strings"
	"testing"
)

// TestDecodeCharsetTranscodes proves the body of a message in a non-Western
// charset survives delivery. The decoded string is the only copy kept (it becomes
// PR_BODY), so reading these bytes as UTF-8 because the name was unfamiliar
// destroys the message for every reader afterwards.
func TestDecodeCharsetTranscodes(t *testing.T) {
	cases := []struct {
		name    string
		charset string
		in      []byte
		want    string
	}{
		{
			// "Привет" in Cyrillic Windows-1251, the charset Russian mail commonly
			// declares.
			name: "windows-1251 Cyrillic", charset: "windows-1251",
			in: []byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2}, want: "Привет",
		},
		{
			// "Καλημέρα" in ISO-8859-7, the Greek single-byte set.
			name: "iso-8859-7 Greek", charset: "iso-8859-7",
			in:   []byte{0xCA, 0xE1, 0xEB, 0xE7, 0xEC, 0xDD, 0xF1, 0xE1},
			want: "Καλημέρα",
		},
		{
			// "Привет" again, this time in KOI8-R, which orders Cyrillic differently.
			name: "koi8-r Cyrillic", charset: "koi8-r",
			in: []byte{0xF0, 0xD2, 0xC9, 0xD7, 0xC5, 0xD4}, want: "Привет",
		},
		{
			// "こんにちは" in Shift_JIS: a multi-byte encoding, which no single-byte
			// table can cover.
			name: "shift_jis Japanese", charset: "shift_jis",
			in:   []byte{0x82, 0xB1, 0x82, 0xF1, 0x82, 0xC9, 0x82, 0xBF, 0x82, 0xCD},
			want: "こんにちは",
		},
		{
			// "中文" in Big5, traditional Chinese.
			name: "big5 Chinese", charset: "big5",
			in: []byte{0xA4, 0xA4, 0xA4, 0xE5}, want: "中文",
		},
		{
			// 0x80 and 0x85 are printable in Windows-1252 (euro sign, ellipsis). The
			// byte-to-rune mapping this replaced turned them into control characters.
			name: "windows-1252 high range", charset: "windows-1252",
			in: []byte{0x80, 0x20, 0x85}, want: "€ …",
		},
		{
			name: "utf-8 passes through", charset: "utf-8",
			in: []byte("héllo"), want: "héllo",
		},
		{
			name: "us-ascii passes through", charset: "us-ascii",
			in: []byte("plain"), want: "plain",
		},
		{
			name: "no charset passes through", charset: "",
			in: []byte("plain"), want: "plain",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DecodeCharset(c.in, c.charset); got != c.want {
				t.Errorf("DecodeCharset(%q) = %q, want %q", c.charset, got, c.want)
			}
		})
	}
}

// TestDecodeCharsetUnknownFallsBack holds the fallback: a name nothing
// recognizes still yields the bytes rather than an empty body or a panic. Showing
// a body imperfectly beats losing it.
func TestDecodeCharsetUnknownFallsBack(t *testing.T) {
	in := []byte("some bytes")
	orig := append([]byte(nil), in...)
	if got := DecodeCharset(in, "x-not-a-real-charset"); got != "some bytes" {
		t.Errorf("unknown charset gave %q, want the raw bytes", got)
	}
	if !bytes.Equal(in, orig) {
		t.Errorf("unknown charset mutated input buffer: got %q, want %q", in, orig)
	}
	oversized := strings.Repeat("x", 4096)
	if got := DecodeCharset(in, oversized); got != "some bytes" {
		t.Errorf("oversized charset gave %q, want the raw bytes", got)
	}
	if got := DecodeCharset(nil, "windows-1251"); got != "" {
		t.Errorf("empty input gave %q", got)
	}
	// Case-insensitive and whitespace-padded charset names must still resolve.
	cyrillic := []byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2}
	if got := DecodeCharset(cyrillic, "  WINDOWS-1251 \t"); got != "Привет" {
		t.Errorf("padded uppercase charset gave %q, want %q", got, "Привет")
	}
}

// TestDecodedContent verifies RFC 2045 transfer decoding for base64,
// quoted-printable, and passthrough encodings, plus refusal of malformed input.
func TestDecodedContent(t *testing.T) {
	// RFC 2045 section 6.8 allows CRLF and whitespace inside base64 bodies.
	b64Part := ParseStructure([]byte("Content-Type: text/plain\r\nContent-Transfer-Encoding: BASE64\r\n\r\nSGVs\r\n bG8=\r\n"))
	got, err := b64Part.DecodedContent()
	mustNoErr(t, err, "decode valid base64")
	wantEq(t, string(got), "Hello", "decoded base64")

	// RFC 2045 section 6.7 soft line break and hex escape.
	qpPart := ParseStructure([]byte("Content-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=C3=A9=\r\n!"))
	got, err = qpPart.DecodedContent()
	mustNoErr(t, err, "decode valid quoted-printable")
	wantEq(t, string(got), "café!", "decoded quoted-printable")

	// 7bit body passes through verbatim.
	plainPart := ParseStructure([]byte("Content-Type: text/plain\r\nContent-Transfer-Encoding: 7bit\r\n\r\nraw body"))
	got, err = plainPart.DecodedContent()
	mustNoErr(t, err, "decode 7bit")
	wantEq(t, string(got), "raw body", "7bit passthrough")

	// Hostile base64 must return an error without mutating the stored raw body.
	badB64Raw := "!!!not-base64!!!"
	badB64 := ParseStructure([]byte("Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n\r\n" + badB64Raw))
	if _, err := badB64.DecodedContent(); err == nil {
		t.Error("DecodedContent(invalid base64) = nil error, want error")
	}
	wantEq(t, string(badB64.RawBody()), badB64Raw, "raw body after base64 failure")

	// Hostile quoted-printable with invalid bytes after '=' (RFC 2045 section 6.7)
	// must fail without mutating the stored raw body.
	badQPRaw := "=\rX"
	badQP := ParseStructure([]byte("Content-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" + badQPRaw))
	if _, err := badQP.DecodedContent(); err == nil {
		t.Error("DecodedContent(invalid quoted-printable) = nil error, want error")
	}
	wantEq(t, string(badQP.RawBody()), badQPRaw, "raw body after quoted-printable failure")
}

// TestDecodedText verifies combined transfer decoding and charset conversion,
// and refusal when transfer decoding fails.
func TestDecodedText(t *testing.T) {
	// "z/Do4uXy" is RFC 4648 base64 of Windows-1251 "Привет" (0xCF 0xF0 0xE8 0xE2 0xE5 0xF2).
	p := ParseStructure([]byte("Content-Type: text/plain; charset=windows-1251\r\nContent-Transfer-Encoding: base64\r\n\r\nz/Do4uXy"))
	text, err := p.DecodedText()
	mustNoErr(t, err, "decode base64 windows-1251 text")
	wantEq(t, text, "Привет", "decoded text")

	badRaw := "@@@"
	bad := ParseStructure([]byte("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + badRaw))
	text, err = bad.DecodedText()
	if err == nil {
		t.Error("DecodedText(invalid base64) = nil error, want error")
	}
	wantEq(t, text, "", "decoded text on error")
	wantEq(t, string(bad.RawBody()), badRaw, "raw body after DecodedText failure")
}

// TestPartFilename verifies Content-Disposition filename precedence, fallback
// to Content-Type name, and RFC 2047 decoding including malformed encoded-words.
func TestPartFilename(t *testing.T) {
	both := ParseStructure([]byte(
		"Content-Type: application/pdf; name=\"fallback.pdf\"\r\n" +
			"Content-Disposition: attachment; filename=\"=?UTF-8?Q?r=C3=A9sum=C3=A9.pdf?=\"\r\n\r\n",
	))
	wantEq(t, both.Filename(), "résumé.pdf", "disposition filename over Content-Type name")

	fallback := ParseStructure([]byte("Content-Type: application/pdf; name=\"report.pdf\"\r\n\r\n"))
	wantEq(t, fallback.Filename(), "report.pdf", "Content-Type name fallback")

	none := ParseStructure([]byte("Content-Type: text/plain\r\n\r\nhi"))
	wantEq(t, none.Filename(), "", "absent filename")

	malformed := ParseStructure([]byte(
		"Content-Disposition: attachment; filename=\"=?X-NO-SUCH-CHARSET?Q?bad?=\"\r\n\r\n",
	))
	wantEq(t, malformed.Filename(), "=?X-NO-SUCH-CHARSET?Q?bad?=", "malformed RFC 2047 filename fallback")
}
