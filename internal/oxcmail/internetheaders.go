package oxcmail

import (
	"net/textproto"
	"sort"
	"strings"

	"hermex/internal/mapi"
)

// A header field no other property maps travels between MIME and MAPI as a string
// named property in the PS_INTERNET_HEADERS set, named after the field
// ([MS-OXCMAIL] 2.2.3.2.x "Generic Headers in PS_INTERNET_HEADERS" on import and
// 2.1.3.2.x "Arbitrary MIME Headers" on export). This is how a client that stores
// an X- header on a message sees it on the wire, and how a received one reaches
// the client.

// maxPromotedHeaders bounds how many fields of one message become named
// properties. A header block is sender-controlled, and every promoted field costs
// a named-property lookup; the store bounds the id space separately.
const maxPromotedHeaders = 100

// mappedHeaders are the fields another property already carries, in lowercase.
// A reader must not promote them and a writer must not emit a named property of
// the same name, because the message would then say two things in one field.
var mappedHeaders = map[string]bool{
	"mime-version": true, "from": true, "sender": true, "to": true, "cc": true,
	"bcc": true, "date": true, "subject": true, "thread-topic": true,
	"message-id": true, "references": true, "in-reply-to": true,
	"importance": true, "x-priority": true, "priority": true,
	"x-msmail-priority": true, "sensitivity": true,
	"disposition-notification-to": true,
}

// unpromotedHeaders are the fields [MS-OXCMAIL] says a reader should not promote,
// in lowercase: trace, transport and content fields, and the news headers.
var unpromotedHeaders = map[string]bool{
	"received": true, "resent-from": true, "resent-sender": true,
	"resent-date": true, "resent-message-id": true, "content-type": true,
	"content-disposition": true, "content-description": true,
	"content-transfer-encoding": true, "content-id": true, "content-md5": true,
	"return-path": true, "comments": true, "adhoc": true, "apparently-to": true,
	"approved": true, "control": true, "distribution": true, "encoding": true,
	"followup-to": true, "lines": true, "bytes": true, "article": true,
	"supercedes": true, "newsgroups": true, "nntppostinghost": true,
	"organization": true, "path": true, "rr": true, "summary": true,
	"trace": true, "encrypted": true, "x-mimeole": true,
	"x-ms-tnef-correlator": true,
}

// reservedHeaderPrefixes are the Exchange-internal families, in lowercase. A reader
// keeps only the four authentication fields of the organization family, and a
// writer emits none of them.
var reservedHeaderPrefixes = []string{
	"x-ms-exchange-organization-", "x-ms-exchange-forest-",
	"x-microsoft-exchange-organization", "x-microsoft-exchange-forest",
}

// keptReservedHeaders are the reserved fields a reader still promotes.
var keptReservedHeaders = map[string]bool{
	"x-ms-exchange-organization-authas":        true,
	"x-ms-exchange-organization-authdomain":    true,
	"x-ms-exchange-organization-authmechanism": true,
	"x-ms-exchange-organization-authsource":    true,
}

// otherwiseCarried reports whether a field reaches the other side without a named
// property: it maps to a property, or Export re-emits it from the stored arrival
// headers.
func otherwiseCarried(low string) bool {
	return mappedHeaders[low] || isPreservedHeader(low)
}

// isReservedHeader reports whether a field belongs to an Exchange-internal family.
func isReservedHeader(low string) bool {
	for _, p := range reservedHeaderPrefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// promotable reports whether a reader turns a received field into a named property.
func promotable(name string) bool {
	low := strings.ToLower(name)
	if otherwiseCarried(low) || unpromotedHeaders[low] {
		return false
	}
	return !isReservedHeader(low) || keptReservedHeaders[low]
}

// exportable reports whether a writer emits a named property as a header field.
// The name must be a valid field name, because it is written verbatim.
func exportable(name string) bool {
	low := strings.ToLower(name)
	if !validFieldName(name) || otherwiseCarried(low) || isReservedHeader(low) {
		return false
	}
	// Content fields describe the MIME structure Export builds itself.
	return !strings.HasPrefix(low, "content-")
}

// validFieldName reports whether name is an RFC 5322 field name: printable ASCII
// without a colon. Anything else would end or corrupt the header line.
func validFieldName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x21 || c > 0x7E || c == ':' {
			return false
		}
	}
	return true
}

// promoteInternetHeaders stores each promotable top-level field as a
// PS_INTERNET_HEADERS named property, its encoded words decoded. A field the store
// declines to name (its quota is spent) is left out; the arrival header block
// still carries it.
func promoteInternetHeaders(hdr textproto.MIMEHeader, msg *Message, opt Options) error {
	keys := promotableKeys(hdr)
	if opt.Resolver == nil || len(keys) == 0 {
		return nil
	}
	names := make([]mapi.PropertyName, len(keys))
	for i, k := range keys {
		names[i] = mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsInternetHeaders, Name: k}
	}
	ids, err := opt.Resolver(true, names)
	if err != nil {
		return err
	}
	for i, id := range ids {
		if id != 0 {
			msg.Props.Set(mapi.MakeTag(id, mapi.PtUnicode), decodeHeaderWord(hdr[keys[i]][0]))
		}
	}
	return nil
}

// promotableKeys returns the promotable field names of a header block in name
// order, at most maxPromotedHeaders of them.
func promotableKeys(hdr textproto.MIMEHeader) []string {
	keys := make([]string, 0, len(hdr))
	for k := range hdr {
		if promotable(k) && len(hdr[k]) > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > maxPromotedHeaders {
		keys = keys[:maxPromotedHeaders]
	}
	return keys
}

// headerField is one field Export writes from a named property.
type headerField struct{ name, value string }

// internetHeaderFields collects the PS_INTERNET_HEADERS string properties a message
// carries, in name order, one field per name.
func internetHeaderFields(props mapi.PropertyValues, opt Options) ([]headerField, error) {
	if opt.PropName == nil {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []headerField
	for _, pv := range props {
		f, ok, err := internetHeaderField(pv, opt.PropName)
		if err != nil {
			return nil, err
		}
		low := strings.ToLower(f.name)
		if !ok || seen[low] {
			continue
		}
		seen[low] = true
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// internetHeaderField reads one property as a header field, reporting false for a
// property that is not an exportable PS_INTERNET_HEADERS string.
func internetHeaderField(pv mapi.TaggedPropVal, nameOf PropNameResolver) (headerField, bool, error) {
	if t := pv.Tag.Type(); (t != mapi.PtUnicode && t != mapi.PtString8) || pv.Tag.ID() < 0x8000 {
		return headerField{}, false, nil
	}
	value, _ := pv.Value.(string)
	if value == "" {
		return headerField{}, false, nil
	}
	name, ok, err := nameOf(pv.Tag.ID())
	if err != nil || !ok {
		return headerField{}, false, err
	}
	if name.Kind != mapi.MnidString || name.GUID != mapi.PsInternetHeaders || !exportable(name.Name) {
		return headerField{}, false, nil
	}
	return headerField{name: name.Name, value: value}, true, nil
}
