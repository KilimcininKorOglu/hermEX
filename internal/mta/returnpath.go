package mta

import (
	"strings"
	"unicode"
)

// withReturnPath is the copy of raw a final delivery files: the envelope sender
// written as the first header field, the way RFC 5321 §4.4 has the delivering
// server record the reverse-path, after every Return-Path field the message
// arrived with is removed. A sender can write that field itself, and a reader that
// trusts the header must see the envelope, never the sender's claim. An empty from
// is the null reverse-path, written as <>. A from holding a control character is
// not written, because the character would end the header line.
func withReturnPath(raw []byte, from string) []byte {
	raw = withoutHeader(raw, "Return-Path")
	if strings.ContainsFunc(from, unicode.IsControl) {
		return raw
	}
	return append([]byte("Return-Path: <"+from+">\r\n"), raw...)
}

// withoutHeader returns raw with every field of the named header removed, each
// with its folded continuation lines. raw is returned as it is when it holds none.
func withoutHeader(raw []byte, name string) []byte {
	for {
		start, end, ok := headerSpan(raw, name)
		if !ok {
			return raw
		}
		out := make([]byte, 0, len(raw)-(end-start))
		out = append(out, raw[:start]...)
		raw = append(out, raw[end:]...)
	}
}
