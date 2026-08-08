package mime

import "testing"

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
	if got := DecodeCharset(in, "x-not-a-real-charset"); got != "some bytes" {
		t.Errorf("unknown charset gave %q, want the raw bytes", got)
	}
	if got := DecodeCharset(nil, "windows-1251"); got != "" {
		t.Errorf("empty input gave %q", got)
	}
}
