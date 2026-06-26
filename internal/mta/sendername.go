package mta

import (
	"bytes"
	"net/mail"
	"strings"
)

// RewriteFromDisplayName replaces the display name of raw's From header with name,
// preserving the address and leaving every other header (including Sender) and the
// body byte-identical. raw with no From, an unparseable From, or an empty name is
// returned unchanged. The address parse and re-emit go through net/mail, so a
// non-ASCII name (Turkish) is RFC 2047 encoded and round-trips. This must run BEFORE
// DKIM signing, since the From header is signed.
func RewriteFromDisplayName(raw []byte, name string) []byte {
	if name == "" {
		return raw
	}
	start, end, ok := headerSpan(raw, "From")
	if !ok {
		return raw
	}
	value := string(raw[start:end])
	if c := strings.IndexByte(value, ':'); c >= 0 {
		value = value[c+1:]
	}
	// Unfold continuation lines so the value parses as a single address.
	value = strings.NewReplacer("\r\n ", " ", "\r\n\t", " ", "\n ", " ", "\n\t", " ").Replace(value)
	addr, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil {
		return raw
	}
	term := "\n"
	if bytes.HasSuffix(raw[start:end], []byte("\r\n")) {
		term = "\r\n"
	}
	newHeader := "From: " + (&mail.Address{Name: name, Address: addr.Address}).String() + term
	out := make([]byte, 0, len(raw)-(end-start)+len(newHeader))
	out = append(out, raw[:start]...)
	out = append(out, newHeader...)
	out = append(out, raw[end:]...)
	return out
}

// headerSpan returns the byte range of the named header within raw's header block
// (the "Name:" line plus any folded continuation lines, including the trailing line
// terminator), matched case-insensitively. ok is false when the header is absent or
// the header block ends first.
func headerSpan(raw []byte, name string) (start, end int, ok bool) {
	prefix := strings.ToLower(name) + ":"
	for i := 0; i < len(raw); {
		lineEnd := i
		for lineEnd < len(raw) && raw[lineEnd] != '\n' {
			lineEnd++
		}
		next := lineEnd
		if next < len(raw) {
			next++ // include the '\n'
		}
		content := bytes.TrimRight(raw[i:next], "\r\n")
		if len(content) == 0 {
			return 0, 0, false // blank line: end of the header block
		}
		if strings.HasPrefix(strings.ToLower(string(content)), prefix) {
			end = next
			for end < len(raw) && (raw[end] == ' ' || raw[end] == '\t') {
				for end < len(raw) && raw[end] != '\n' {
					end++
				}
				if end < len(raw) {
					end++
				}
			}
			return i, end, true
		}
		i = next
	}
	return 0, 0, false
}
