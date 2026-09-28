package oxcmail

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// nameOf reverses an in-memory named-property table, as the store's NamedPropName does.
func nameOf(names map[mapi.PropertyName]uint16) PropNameResolver {
	return func(id uint16) (mapi.PropertyName, bool, error) {
		for n, v := range names {
			if v == id {
				return n, true, nil
			}
		}
		return mapi.PropertyName{}, false, nil
	}
}

// internetHeader reads a PS_INTERNET_HEADERS property of the given field name.
func internetHeader(props mapi.PropertyValues, names map[mapi.PropertyName]uint16, field string) (string, bool) {
	id, ok := names[mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsInternetHeaders, Name: field}]
	if !ok {
		return "", false
	}
	v, ok := props.Get(mapi.MakeTag(id, mapi.PtUnicode))
	s, _ := v.(string)
	return s, ok
}

const unmappedHeadersMail = "From: sender@example.org\r\nTo: recipient@example.org\r\n" +
	"Subject: headers\r\nReceived: from relay.example.org\r\nX-Spam-Status: No\r\n" +
	"X-Ms-Exchange-Organization-Scl: 1\r\nX-Ms-Exchange-Organization-Authas: Internal\r\n" +
	"X-Ticket: =?utf-8?q?caf=C3=A9?=\r\n\r\nbody\r\n"

// TestImportPromotesUnmappedHeaders proves a received field no property carries
// becomes a PS_INTERNET_HEADERS property with its encoded words decoded, while a
// mapped field, a trace field, a field Export already re-emits and an
// Exchange-internal field stay out ([MS-OXCMAIL] Generic Headers in
// PS_INTERNET_HEADERS).
func TestImportPromotesUnmappedHeaders(t *testing.T) {
	resolver, names := namedResolver()
	msg, err := Import([]byte(unmappedHeadersMail), Options{Resolver: resolver})
	mustImport(t, err)

	if v, _ := internetHeader(msg.Props, names, "X-Ticket"); v != "café" {
		t.Errorf("X-Ticket = %q, want the decoded café", v)
	}
	if _, ok := internetHeader(msg.Props, names, "X-Ms-Exchange-Organization-Authas"); !ok {
		t.Error("the organization AuthAs field is one a reader keeps")
	}
	for _, field := range []string{"Subject", "From", "Received", "X-Spam-Status", "X-Ms-Exchange-Organization-Scl"} {
		if _, ok := internetHeader(msg.Props, names, field); ok {
			t.Errorf("%s must not become a PS_INTERNET_HEADERS property", field)
		}
	}
}

// TestExportWritesInternetHeaderProperties proves a client-stored
// PS_INTERNET_HEADERS string reaches the wire as its own field, RFC 2047 encoded,
// and that a stored value cannot splice further fields into the message.
func TestExportWritesInternetHeaderProperties(t *testing.T) {
	resolver, names := namedResolver()
	ids, _ := resolver(true, []mapi.PropertyName{
		{Kind: mapi.MnidString, GUID: mapi.PsInternetHeaders, Name: "X-Ticket"},
		{Kind: mapi.MnidString, GUID: mapi.PsInternetHeaders, Name: "X-Injected"},
	})
	msg := &Message{}
	msg.Props.Set(mapi.PrSubject, "headers")
	msg.Props.Set(mapi.MakeTag(ids[0], mapi.PtUnicode), "café")
	msg.Props.Set(mapi.MakeTag(ids[1], mapi.PtUnicode), "a\r\nBcc: victim@example.org")

	raw, err := Export(msg, Options{Resolver: resolver, PropName: nameOf(names)})
	mustImport(t, err)
	out := string(raw)
	if !strings.Contains(out, "X-Ticket: =?utf-8?q?caf=C3=A9?=\r\n") {
		t.Errorf("the stored field is missing or unencoded:\n%s", out)
	}
	if strings.Contains(out, "\r\nBcc:") {
		t.Errorf("a stored value injected a header field:\n%s", out)
	}
}

// TestExportSkipsInternetHeadersAnotherPropertyWrites proves a named property never
// doubles a field Export writes from its own property, nor writes an
// Exchange-internal field or a name that is not a valid field name.
func TestExportSkipsInternetHeadersAnotherPropertyWrites(t *testing.T) {
	resolver, names := namedResolver()
	fields := []string{"Subject", "Content-Type", "X-Ms-Exchange-Organization-Scl", "Bad Name", "X-Evil:Field"}
	pn := make([]mapi.PropertyName, len(fields))
	for i, f := range fields {
		pn[i] = mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsInternetHeaders, Name: f}
	}
	ids, _ := resolver(true, pn)
	msg := &Message{}
	msg.Props.Set(mapi.PrSubject, "real subject")
	for _, id := range ids {
		msg.Props.Set(mapi.MakeTag(id, mapi.PtUnicode), "stored")
	}

	raw, err := Export(msg, Options{Resolver: resolver, PropName: nameOf(names)})
	mustImport(t, err)
	if out := string(raw); strings.Contains(out, "stored") {
		t.Errorf("a field another property writes, or an unwritable name, was exported:\n%s", out)
	}
}
