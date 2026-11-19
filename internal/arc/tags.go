package arc

import (
	"strings"
)

// tagList parses a DKIM-style tag-list value (RFC 6376 section 3.2) into its
// tags. Whitespace, folding included, is removed from every value, which is
// what the base64 and colon-separated values carried here need.
func tagList(value string) map[string]string {
	tags := map[string]string{}
	for spec := range strings.SplitSeq(value, ";") {
		name, val, ok := strings.Cut(spec, "=")
		if !ok {
			continue
		}
		tags[strings.TrimSpace(name)] = strings.Join(strings.Fields(val), "")
	}
	return tags
}

// fieldValue returns the value of a raw header field, after the colon.
func fieldValue(raw string) string {
	_, v, _ := strings.Cut(raw, ":")
	return v
}

// withoutSignature returns a raw signature field with the value of its b= tag
// removed and everything else kept as it is, the form the signature is computed
// over (RFC 6376 section 3.5). The bh= tag is a different tag and is kept.
func withoutSignature(raw string) string {
	name, value, _ := strings.Cut(raw, ":")
	specs := strings.Split(value, ";")
	for i, spec := range specs {
		tag, _, ok := strings.Cut(spec, "=")
		if ok && strings.TrimSpace(tag) == "b" {
			specs[i] = spec[:strings.IndexByte(spec, '=')+1]
		}
	}
	return name + ":" + strings.Join(specs, ";")
}
