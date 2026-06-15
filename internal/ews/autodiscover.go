package ews

import (
	"encoding/xml"
	"io"
	"net/http"
)

// maxAutodiscoverBody caps the Autodiscover request body read.
const maxAutodiscoverBody = 1 << 16

// autodiscoverResponse is the Outlook-schema Autodiscover reply: the outer
// Autodiscover element and the inner Response live in different namespaces
// (the outer responseschema/2006, the inner outlook/responseschema/2006a).
type autodiscoverResponse struct {
	XMLName  xml.Name        `xml:"http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006 Autodiscover"`
	Response outlookResponse `xml:"http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a Response"`
}

type outlookResponse struct {
	User    adUser    `xml:"User"`
	Account adAccount `xml:"Account"`
}

type adUser struct {
	DisplayName             string `xml:"DisplayName"`
	AutoDiscoverSMTPAddress string `xml:"AutoDiscoverSMTPAddress"`
}

type adAccount struct {
	AccountType string     `xml:"AccountType"`
	Action      string     `xml:"Action"`
	Protocol    adProtocol `xml:"Protocol"`
}

type adProtocol struct {
	Type   string `xml:"Type"`
	Server string `xml:"Server"`
	EwsURL string `xml:"EwsUrl"`
	// Outlook Anywhere (RPC/HTTP, [MS-OXDSCLI] 2.2.4.1.1.2.3) advertising: the
	// EXPR provider's Server is the RPC proxy host, so a desktop Outlook builds
	// https://<Server>/rpc/rpcproxy.dll and connects with HTTP Basic over TLS.
	SSL                    string `xml:"SSL,omitempty"`
	AuthPackage            string `xml:"AuthPackage,omitempty"`
	ServerExclusiveConnect string `xml:"ServerExclusiveConnect,omitempty"`
}

// serveAutodiscover answers the Outlook Autodiscover request (MS-OXDSCLI): it
// authenticates, then returns the EWS endpoint URL the client should bind to.
// The authenticated identity is authoritative for the echoed address; the
// request body is drained but not trusted.
func (s *Server) serveAutodiscover(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.basicAuth(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxAutodiscoverBody))

	host := s.hostname
	if host == "" {
		host = r.Host
	}
	resp := autodiscoverResponse{
		Response: outlookResponse{
			User: adUser{DisplayName: user, AutoDiscoverSMTPAddress: user},
			Account: adAccount{
				AccountType: "email",
				Action:      "settings",
				Protocol: adProtocol{
					Type:                   "EXPR",
					Server:                 host,
					EwsURL:                 "https://" + host + "/EWS/Exchange.asmx",
					SSL:                    "On",
					AuthPackage:            "Basic",
					ServerExclusiveConnect: "On",
				},
			},
		},
	}
	out, err := xml.Marshal(&resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}
