package ews

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"sync/atomic"
)

// defaultMaxRequestBody caps a SOAP request body; it is the fallback when no operator
// limit has been set.
const defaultMaxRequestBody = 8 << 20

// reqBodyLimit holds the operator-set SOAP request-body cap (bytes; 0 = use the
// default), set by SetMaxRequestBody and read live by readEnvelope, so the EWS daemon's
// poll can apply an edit without a restart. The EWS service is a per-process singleton,
// so a package-level value is the right scope (mirrors objectstore's default logger).
var reqBodyLimit atomic.Int64

// SetMaxRequestBody sets the maximum accepted SOAP request body in bytes (0 restores
// the built-in default). It is safe to call concurrently with request handling, so an
// operator's edit applies without a restart.
func SetMaxRequestBody(n int64) {
	if n < 0 {
		n = 0
	}
	reqBodyLimit.Store(n)
}

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

// soapEnvelope decodes just enough of a request envelope: the SOAP header (for an
// optional ExchangeImpersonation directive) and the raw operation element from the
// body. The operation is left as innerxml so each handler unmarshals its own
// request type.
type soapEnvelope struct {
	XMLName xml.Name   `xml:"http://schemas.xmlsoap.org/soap/envelope/ Envelope"`
	Header  soapHeader `xml:"http://schemas.xmlsoap.org/soap/envelope/ Header"`
	Body    struct {
		Inner []byte `xml:",innerxml"`
	} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
}

// soapHeader holds the request SOAP header fields hermEX reads. Only
// ExchangeImpersonation is consulted; its ConnectingSID names the user to
// impersonate. The inner element tags are matched by local name (Go's xml matches
// across namespaces on input), so the types-namespace prefix the client uses does
// not have to be reproduced here.
type soapHeader struct {
	Impersonation *struct {
		ConnectingSID connectingSID `xml:"ConnectingSID"`
	} `xml:"ExchangeImpersonation"`
}

// connectingSID is the ConnectingSID choice ([MS-OXWSCDATA] 2.2.4.14): the
// account to impersonate, named by exactly one of these forms. hermEX resolves
// the address forms against the directory; a bare SID has no directory to resolve
// against and is reported unsupported.
type connectingSID struct {
	PrincipalName      string `xml:"PrincipalName"`
	PrimarySmtpAddress string `xml:"PrimarySmtpAddress"`
	SmtpAddress        string `xml:"SmtpAddress"`
	SID                string `xml:"SID"`
}

// target reduces a ConnectingSID to the address to impersonate, or flags a
// SID-only header as unsupported. It returns nil when no usable identity is
// present (an empty ConnectingSID is treated as no impersonation).
func (h soapHeader) target() *impersonationTarget {
	if h.Impersonation == nil {
		return nil
	}
	c := h.Impersonation.ConnectingSID
	switch {
	case c.PrimarySmtpAddress != "":
		return &impersonationTarget{addr: c.PrimarySmtpAddress}
	case c.SmtpAddress != "":
		return &impersonationTarget{addr: c.SmtpAddress}
	case c.PrincipalName != "":
		return &impersonationTarget{addr: c.PrincipalName}
	case c.SID != "":
		return &impersonationTarget{isSID: true}
	default:
		return nil
	}
}

// readEnvelope reads and parses the SOAP request, returning the operation name
// (the local name of the first child of soap:Body), the operation element's raw
// XML for the handler to unmarshal, and the ExchangeImpersonation target (nil when
// the header is absent).
func readEnvelope(r *http.Request) (op string, inner []byte, imp *impersonationTarget, err error) {
	limit := int64(defaultMaxRequestBody)
	if v := reqBodyLimit.Load(); v > 0 {
		limit = v
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return "", nil, nil, err
	}
	var env soapEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return "", nil, nil, err
	}
	return firstElementName(env.Body.Inner), env.Body.Inner, env.Header.target(), nil
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

// writeResponse marshals an operation response struct and wraps it in the SOAP
// envelope. On a marshal error it falls back to a SOAP Fault.
func writeResponse(w http.ResponseWriter, v any) {
	out, err := xml.Marshal(v)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return
	}
	writeSOAP(w, out)
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
