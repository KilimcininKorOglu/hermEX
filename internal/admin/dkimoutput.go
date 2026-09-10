package admin

import (
	"strings"

	"hermex/internal/dkimsign"
)

// The output modes the DKIM panel offers for one published key. They differ only in
// presentation: every mode shows the same public key, and none of them reveals the
// private key.
//
//   - dkimOutputRecord is a zone-file line, ready to paste into a zone the operator edits
//     by hand.
//   - dkimOutputTXT is the bare TXT value, which is what a hosted DNS panel asks for.
//   - dkimOutputKey is the base64 public key alone, for a provider whose form builds the
//     v= and k= tags itself.
const (
	dkimOutputRecord = "record"
	dkimOutputTXT    = "txt"
	dkimOutputKey    = "key"
)

// txtStringLimit is the longest character string a DNS TXT record can carry (RFC 1035
// section 3.3.14). An RSA-2048 record value is roughly 390 characters, so a zone-file line
// must split it into several quoted strings, which the resolver concatenates.
const txtStringLimit = 255

// dkimOutput renders a domain's published DKIM record in the given output mode. An unknown
// mode renders the zone-file line, the mode the panel opens in.
func dkimOutput(mode, recordName, publicTXT string) string {
	if publicTXT == "" {
		return ""
	}
	switch mode {
	case dkimOutputTXT:
		return publicTXT
	case dkimOutputKey:
		return dkimsign.PublicKeyPayload(publicTXT)
	default:
		return recordName + ". IN TXT " + quoteTXT(publicTXT)
	}
}

// quoteTXT splits a TXT value into quoted character strings of at most txtStringLimit
// bytes. A value that fits in one string is quoted on its own; a longer one is wrapped in
// the parentheses a zone file needs around a multi-string record.
func quoteTXT(value string) string {
	chunks := chunkTXT(value)
	if len(chunks) == 1 {
		return `"` + chunks[0] + `"`
	}
	var b strings.Builder
	b.WriteString("( ")
	for _, chunk := range chunks {
		b.WriteString(`"`)
		b.WriteString(chunk)
		b.WriteString(`" `)
	}
	b.WriteString(")")
	return b.String()
}

// chunkTXT cuts a TXT value into pieces of at most txtStringLimit bytes. The value is
// base64 and tag text, so cutting on byte count never splits a multi-byte rune.
func chunkTXT(value string) []string {
	var chunks []string
	for len(value) > txtStringLimit {
		chunks = append(chunks, value[:txtStringLimit])
		value = value[txtStringLimit:]
	}
	return append(chunks, value)
}
