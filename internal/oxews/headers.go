package oxews

import (
	"strings"

	"hermex/internal/mapi"
)

// HeaderList is the EWS <t:InternetMessageHeaders> element: the message's header
// block, one entry per header field in the order the message carries them.
type HeaderList struct {
	Headers []InternetHeader `xml:"InternetMessageHeader"`
}

// InternetHeader is one <t:InternetMessageHeader>. HeaderName is a required
// attribute, so it is written even when empty: a client that unescapes it without
// testing for absence would otherwise work on a missing value.
type InternetHeader struct {
	Name  string `xml:"HeaderName,attr"`
	Value string `xml:",chardata"`
}

// MessageHeaders renders the stored transport header block
// (PidTagTransportMessageHeaders) as InternetMessageHeaders, or nil when the item
// carries none, as a message composed rather than received does.
func MessageHeaders(props mapi.PropertyValues) *HeaderList {
	v, _ := props.GetAnyCharset(mapi.PrTransportMessageHeaders)
	block, _ := v.(string)
	var list HeaderList
	for _, field := range headerFields(block) {
		name, value, _ := strings.Cut(field, ":")
		list.Headers = append(list.Headers, InternetHeader{
			Name:  strings.TrimSpace(name),
			Value: strings.TrimSpace(value),
		})
	}
	if len(list.Headers) == 0 {
		return nil
	}
	return &list
}

// headerFields splits a header block into its fields, joining each folded
// continuation line onto the field it continues (RFC 5322 2.2.3). The block ends
// at the first empty line.
func headerFields(block string) []string {
	var fields []string
	for line := range strings.Lines(block) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if (line[0] == ' ' || line[0] == '\t') && len(fields) > 0 {
			fields[len(fields)-1] += " " + strings.TrimSpace(line)
			continue
		}
		fields = append(fields, line)
	}
	return fields
}
