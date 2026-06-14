package ews

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
)

// maxRequestBody caps a SOAP request body.
const maxRequestBody = 8 << 20

// EWS XML namespaces (MS-OXWS). Clients are namespace-aware (they match on the
// URI, not the prefix), so responses declare these as the relevant element's
// namespace and the exact prefix string does not matter.
const (
	nsSOAP     = "http://schemas.xmlsoap.org/soap/envelope/"
	nsTypes    = "http://schemas.microsoft.com/exchange/services/2006/types"
	nsMessages = "http://schemas.microsoft.com/exchange/services/2006/messages"
)

// serverVersion is the single EWS schema version v1 advertises in every response
// header's ServerVersionInfo. Exchange2010_SP2 (14.2) is EWS-complete and needs
// no MAPI/ROP, so it is the honest floor for a mail-only EWS server.
const serverVersion = "Exchange2010_SP2"

// soapEnvelope decodes just enough of a request envelope: the requested server
// version from the header and the raw operation element from the body. The
// operation is left as innerxml so each handler unmarshals its own request type.
type soapEnvelope struct {
	XMLName xml.Name `xml:"http://schemas.xmlsoap.org/soap/envelope/ Envelope"`
	Body    struct {
		Inner []byte `xml:",innerxml"`
	} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
}

// readEnvelope reads and parses the SOAP request, returning the operation name
// (the local name of the first child of soap:Body) and the operation element's
// raw XML for the handler to unmarshal.
func readEnvelope(r *http.Request) (op string, inner []byte, err error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		return "", nil, err
	}
	var env soapEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return "", nil, err
	}
	return firstElementName(env.Body.Inner), env.Body.Inner, nil
}

// firstElementName returns the local name of the first XML element in a fragment,
// or "" if there is none.
func firstElementName(fragment []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(fragment))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}

// soapEnvelopeOpen / soapEnvelopeClose wrap an operation response. The header
// carries ServerVersionInfo (clients reject a response without it); the
// soap/types/messages namespaces are declared once on the envelope so response
// bodies need no per-element namespace boilerplate.
const soapEnvelopeOpen = xml.Header +
	`<soap:Envelope xmlns:soap="` + nsSOAP + `"` +
	` xmlns:t="` + nsTypes + `"` +
	` xmlns:m="` + nsMessages + `">` +
	`<soap:Header><t:ServerVersionInfo MajorVersion="14" MinorVersion="2"` +
	` MajorBuildNumber="390" MinorBuildNumber="3" Version="` + serverVersion + `"/></soap:Header>` +
	`<soap:Body>`

const soapEnvelopeClose = `</soap:Body></soap:Envelope>`

// writeSOAP wraps a marshalled operation response in the SOAP envelope and writes
// it with the EWS content type.
func writeSOAP(w http.ResponseWriter, innerXML []byte) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = io.WriteString(w, soapEnvelopeOpen)
	_, _ = w.Write(innerXML)
	_, _ = io.WriteString(w, soapEnvelopeClose)
}

// writeSOAPFault writes an envelope-level SOAP 1.1 Fault carrying an EWS
// response code in the detail. Used for malformed envelopes and unsupported
// operations (the request never reaches a per-operation response message).
func writeSOAPFault(w http.ResponseWriter, code, faultstring string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, xml.Header)
	_, _ = io.WriteString(w, `<soap:Envelope xmlns:soap="`+nsSOAP+`">`+
		`<soap:Body><soap:Fault>`+
		`<faultcode>soap:Client</faultcode>`+
		`<faultstring xml:lang="en-US">`)
	xmlEscape(w, faultstring)
	_, _ = io.WriteString(w, `</faultstring>`+
		`<detail><m:ResponseCode xmlns:m="`+nsMessages+`">`+code+`</m:ResponseCode></detail>`+
		`</soap:Fault></soap:Body></soap:Envelope>`)
}

// xmlEscape writes s with XML special characters escaped.
func xmlEscape(w io.Writer, s string) {
	_ = xml.EscapeText(w, []byte(s))
}
