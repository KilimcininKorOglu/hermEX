package ews

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/url"

	"hermex/internal/logging"
	"hermex/internal/serve"
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
	AccountType string       `xml:"AccountType"`
	Action      string       `xml:"Action"`
	Protocol    []adProtocol `xml:"Protocol"`
}

// adProtocol is one Protocol element ([MS-OXDSCLI] 2.2.4.1.1.2.3). The classic
// providers name their type in a Type child; MAPI/HTTP names it in Type and
// Version attributes and carries MailStore and AddressBook URLs instead.
type adProtocol struct {
	TypeAttr    string       `xml:"Type,attr,omitempty"`
	Version     string       `xml:"Version,attr,omitempty"`
	Type        string       `xml:"Type,omitempty"`
	Server      string       `xml:"Server,omitempty"`
	Port        int          `xml:"Port,omitempty"`
	LoginName   string       `xml:"LoginName,omitempty"`
	EwsURL      string       `xml:"EwsUrl,omitempty"`
	MailStore   *adEndpoints `xml:"MailStore,omitempty"`
	AddressBook *adEndpoints `xml:"AddressBook,omitempty"`
	// Outlook Anywhere (RPC/HTTP, [MS-OXDSCLI] 2.2.4.1.1.2.3) advertising: the
	// EXPR provider's Server is the RPC proxy host, so a desktop Outlook builds
	// https://<Server>/rpc/rpcproxy.dll and connects with HTTP Basic over TLS.
	SSL                    string `xml:"SSL,omitempty"`
	AuthPackage            string `xml:"AuthPackage,omitempty"`
	ServerExclusiveConnect string `xml:"ServerExclusiveConnect,omitempty"`
	// Outlook opens EcpUrl joined with EcpUrl-photo when the user asks to change
	// the profile picture, so the pair names the webmail settings page.
	EcpURL      string `xml:"EcpUrl,omitempty"`
	EcpURLPhoto string `xml:"EcpUrl-photo,omitempty"`
	// Internet-protocol providers: Encryption names the transport security
	// ("SSL" for implicit TLS) and AuthRequired marks SMTP as authenticated.
	Encryption   string `xml:"Encryption,omitempty"`
	AuthRequired string `xml:"AuthRequired,omitempty"`
	SPA          string `xml:"SPA,omitempty"`
}

// adEndpoints is a MAPI/HTTP MailStore or AddressBook URL pair.
type adEndpoints struct {
	InternalURL string `xml:"InternalUrl"`
	ExternalURL string `xml:"ExternalUrl"`
}

// accountProtocols lists what a client may connect with: MAPI/HTTP first when
// the client said it speaks it (X-MapiHttpCapability, [MS-OXDSCLI] 2.2.2.1),
// then Outlook Anywhere, then IMAP, POP3 and authenticated SMTP over implicit
// TLS. Only TLS endpoints are advertised.
func accountProtocols(host, user string, mapiHTTP bool) []adProtocol {
	base := "https://" + host
	id := url.QueryEscape(user)
	var out []adProtocol
	if mapiHTTP {
		out = append(out, adProtocol{
			TypeAttr: "mapiHttp", Version: "1",
			MailStore:   &adEndpoints{base + "/mapi/emsmdb/?MailboxId=" + id, base + "/mapi/emsmdb/?MailboxId=" + id},
			AddressBook: &adEndpoints{base + "/mapi/nspi/?MailboxId=" + id, base + "/mapi/nspi/?MailboxId=" + id},
		})
	}
	out = append(out, adProtocol{
		Type: "EXPR", Server: host, EwsURL: base + "/EWS/Exchange.asmx",
		SSL: "On", AuthPackage: "Basic", ServerExclusiveConnect: "On",
		EcpURL: base + "/", EcpURLPhoto: ecpPhotoPath,
	})
	inet := func(typ string, port int) adProtocol {
		return adProtocol{Type: typ, Server: host, Port: port, LoginName: user, SSL: "On", Encryption: "SSL", SPA: "Off"}
	}
	smtp := inet("SMTP", 465)
	smtp.AuthRequired = "On"
	return append(out, inet("IMAP", 993), inet("POP3", 995), smtp)
}

// ecpPhotoPath is the webmail page, relative to the web frontend root, where
// the user changes the profile picture.
const ecpPhotoPath = "settings"

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
		// The configured hostname is authoritative; fall back to the request Host
		// only when it is a valid public FQDN, since that header is client-supplied
		// and must not steer a client at an attacker-chosen address.
		if serve.ValidPublicHost(r.Host) {
			host = r.Host
		} else {
			http.Error(w, "autodiscover unavailable: server hostname not configured", http.StatusServiceUnavailable)
			return
		}
	}
	resp := autodiscoverResponse{
		Response: outlookResponse{
			User: adUser{DisplayName: user, AutoDiscoverSMTPAddress: user},
			Account: adAccount{
				AccountType: "email",
				Action:      "settings",
				Protocol:    accountProtocols(host, user, r.Header.Get("X-MapiHttpCapability") != ""),
			},
		},
	}
	out, err := xml.Marshal(&resp)
	if err != nil {
		s.Logger.Emit(logging.Event{
			Level:      logging.LevelError,
			Subsystem:  logging.EWS,
			Name:       "autodiscover.marshal.fail",
			RemoteAddr: serve.ClientAddr(r),
			Err:        err.Error(),
		})
		http.Error(w, "an internal error occurred", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}
