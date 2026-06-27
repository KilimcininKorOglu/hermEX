package ews

import (
	"encoding/xml"
	"io"
	"net/http"
)

// SOAP Autodiscover ([MS-OXWSADISC]) is the discovery transport Outlook uses to
// find a mailbox's EWS endpoint. It is distinct from the Outlook-schema POX
// Autodiscover (autodiscover.xml, served above): the request and response are
// SOAP, and the messages live in their own namespace, separate from the EWS
// types/messages namespaces. The response additionally uses WS-Addressing for the
// Action header and XML-Schema-instance for the i:type / i:nil attributes the wire
// format carries on each setting.
const (
	nsAutodiscover = "http://schemas.microsoft.com/exchange/2010/Autodiscover"
	nsWSAddressing = "http://www.w3.org/2005/08/addressing"
	nsXSI          = "http://www.w3.org/2001/XMLSchema-instance"
)

// adSoapRequest decodes a SOAP Autodiscover request envelope far enough to tell a
// GetUserSettings request from the other autodiscover operations and to read the
// requested settings. Element local names are matched across namespaces (Go's xml
// matches on input regardless of prefix), so the a: autodiscover prefix the client
// uses need not be reproduced.
type adSoapRequest struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		UserReq   *adUserSettingsRequest   `xml:"GetUserSettingsRequestMessage"`
		DomainReq *adDomainSettingsRequest `xml:"GetDomainSettingsRequestMessage"`
	} `xml:"Body"`
}

// adUserSettingsRequest is the GetUserSettingsRequestMessage body: the mailboxes to
// describe and the per-mailbox settings the client wants returned.
type adUserSettingsRequest struct {
	Request struct {
		Users struct {
			User []struct {
				Mailbox string `xml:"Mailbox"`
			} `xml:"User"`
		} `xml:"Users"`
		RequestedSettings struct {
			Setting []string `xml:"Setting"`
		} `xml:"RequestedSettings"`
	} `xml:"Request"`
}

// adDomainSettingsRequest is the GetDomainSettingsRequestMessage body: the domains
// to describe and the per-domain settings the client wants returned.
type adDomainSettingsRequest struct {
	Request struct {
		Domains struct {
			Domain []string `xml:"Domain"`
		} `xml:"Domains"`
		RequestedSettings struct {
			Setting []string `xml:"Setting"`
		} `xml:"RequestedSettings"`
	} `xml:"Request"`
}

// serveAutodiscoverSOAP answers the SOAP Autodiscover endpoint
// (/autodiscover/autodiscover.svc, [MS-OXWSADISC]). It authenticates, then for a
// GetUserSettings request returns the per-user settings hermEX can speak for: the
// EWS endpoint URL (host-global) and the caller's own identity. As with POX
// Autodiscover the authenticated identity is authoritative for the echoed address;
// the request body's Mailbox is not trusted, so the response never reveals another
// user's settings.
func (s *Server) serveAutodiscoverSOAP(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.basicAuth(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAutodiscoverBody))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req adSoapRequest
	if err := xml.Unmarshal(body, &req); err != nil {
		http.Error(w, "could not parse Autodiscover request", http.StatusBadRequest)
		return
	}

	host := s.hostname
	if host == "" {
		host = r.Host
	}
	switch {
	case req.Body.UserReq != nil:
		s.writeUserSettings(w, host, user, req.Body.UserReq)
	case req.Body.DomainReq != nil:
		s.writeDomainSettings(w, host, req.Body.DomainReq)
	default:
		http.Error(w, "unsupported Autodiscover request", http.StatusBadRequest)
	}
}

// writeUserSettings emits the GetUserSettingsResponseMessage. One UserResponse is
// returned per requested User (one when the request named none); each carries the
// requested settings hermEX can answer, omitting any it has no value for (an
// unavailable setting is simply absent, which the client tolerates).
func (s *Server) writeUserSettings(w http.ResponseWriter, host, user string, req *adUserSettingsRequest) {
	ewsURL := "https://" + host + "/EWS/Exchange.asmx"
	n := len(req.Request.Users.User)
	if n == 0 {
		n = 1
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = io.WriteString(w, adResponseOpen("GetUserSettingsResponseMessage", "GetUserSettingsResponse"))
	_, _ = io.WriteString(w, "<UserResponses>")
	for i := 0; i < n; i++ {
		_, _ = io.WriteString(w, "<UserResponse>"+
			"<ErrorCode>NoError</ErrorCode>"+
			"<ErrorMessage>No error.</ErrorMessage>"+
			`<RedirectTarget i:nil="true"/>`+
			"<UserSettingErrors/>"+
			"<UserSettings>")
		for _, name := range req.Request.RequestedSettings.Setting {
			if v, ok := userSettingValue(name, ewsURL, user); ok {
				writeADSetting(w, "UserSetting", "StringSetting", name, v)
			}
		}
		_, _ = io.WriteString(w, "</UserSettings></UserResponse>")
	}
	_, _ = io.WriteString(w, "</UserResponses>")
	_, _ = io.WriteString(w, adResponseClose("GetUserSettingsResponseMessage"))
}

// writeDomainSettings emits the GetDomainSettingsResponseMessage. One DomainResponse
// is returned per requested Domain (one when the request named none); each carries
// the requested domain settings hermEX can answer, omitting any it has no value for.
// Domain settings carry the DomainStringSetting type, distinct from the user form.
func (s *Server) writeDomainSettings(w http.ResponseWriter, host string, req *adDomainSettingsRequest) {
	ewsURL := "https://" + host + "/EWS/Exchange.asmx"
	n := len(req.Request.Domains.Domain)
	if n == 0 {
		n = 1
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = io.WriteString(w, adResponseOpen("GetDomainSettingsResponseMessage", "GetDomainSettingsResponse"))
	_, _ = io.WriteString(w, "<DomainResponses>")
	for i := 0; i < n; i++ {
		_, _ = io.WriteString(w, "<DomainResponse>"+
			"<ErrorCode>NoError</ErrorCode>"+
			"<ErrorMessage>No error.</ErrorMessage>"+
			"<DomainSettingErrors/>"+
			"<DomainSettings>")
		for _, name := range req.Request.RequestedSettings.Setting {
			if v, ok := domainSettingValue(name, ewsURL); ok {
				writeADSetting(w, "DomainSetting", "DomainStringSetting", name, v)
			}
		}
		_, _ = io.WriteString(w, "</DomainSettings></DomainResponse>")
	}
	_, _ = io.WriteString(w, "</DomainResponses>")
	_, _ = io.WriteString(w, adResponseClose("GetDomainSettingsResponseMessage"))
}

// userSettingValue maps a requested setting name to the value hermEX serves for it,
// or ok=false when hermEX has no value (the setting is then omitted from the
// response). The EWS URLs are host-global; the identity settings echo the
// authenticated caller, never the request body's mailbox.
func userSettingValue(name, ewsURL, user string) (string, bool) {
	switch name {
	case "ExternalEwsUrl", "InternalEwsUrl":
		return ewsURL, true
	case "AutoDiscoverSMTPAddress", "UserDisplayName":
		return user, true
	case "EwsSupportedSchemas":
		// The schema ladder a 14.2 (Exchange2010_SP2) server supports, matching the
		// version advertised in every EWS response's ServerVersionInfo.
		return "Exchange2007, Exchange2007_SP1, Exchange2010, Exchange2010_SP1, Exchange2010_SP2", true
	}
	return "", false
}

// adResponseOpen builds the SOAP Autodiscover response prologue up to (and
// including) the outer Response element's NoError status: the s:Envelope, the
// WS-Addressing Action header, the default-namespaced message element, and the
// Response element declaring the i: (XML-Schema-instance) prefix the settings use.
func adResponseOpen(msgElem, action string) string {
	return xml.Header +
		`<s:Envelope xmlns:s="` + nsSOAP + `" xmlns:a="` + nsWSAddressing + `">` +
		`<s:Header><a:Action s:mustUnderstand="1">` + nsAutodiscover + `/Autodiscover/` + action + `</a:Action></s:Header>` +
		`<s:Body><` + msgElem + ` xmlns="` + nsAutodiscover + `">` +
		`<Response xmlns:i="` + nsXSI + `">` +
		`<ErrorCode>NoError</ErrorCode><ErrorMessage/>`
}

// adResponseClose closes the Response, message, body, and envelope opened by
// adResponseOpen.
func adResponseClose(msgElem string) string {
	return `</Response></` + msgElem + `></s:Body></s:Envelope>`
}

// domainSettingValue maps a requested domain setting name to the value hermEX
// serves for it, or ok=false when hermEX has no value (the setting is then omitted).
// The EWS URLs are host-global, so a domain query answers them the same as a user
// query does.
func domainSettingValue(name, ewsURL string) (string, bool) {
	switch name {
	case "ExternalEwsUrl", "InternalEwsUrl":
		return ewsURL, true
	}
	return "", false
}

// writeADSetting writes one setting element (UserSetting or DomainSetting) in the
// StringSetting form the wire format uses, carrying the given xsi:type (StringSetting
// for user settings, DomainStringSetting for domain settings). The name is a fixed
// vocabulary string; the value (which may carry an address or URL) is XML-escaped.
func writeADSetting(w io.Writer, elem, xsiType, name, value string) {
	_, _ = io.WriteString(w, "<"+elem+` i:type="`+xsiType+`"><Name>`+name+"</Name><Value>")
	xmlEscape(w, value)
	_, _ = io.WriteString(w, "</Value></"+elem+">")
}
