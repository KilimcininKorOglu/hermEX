package activesync

import (
	"encoding/xml"
	"io"
	"net/http"
)

// maxAutodiscoverBody caps the Autodiscover request body read.
const maxAutodiscoverBody = 1 << 16

// autodiscoverResponse is the mobilesync Autodiscover reply: the outer
// Autodiscover element and the inner Response live in different namespaces.
type autodiscoverResponse struct {
	XMLName  xml.Name           `xml:"http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006 Autodiscover"`
	Response mobileSyncResponse `xml:"http://schemas.microsoft.com/exchange/autodiscover/mobilesync/responseschema/2006 Response"`
}

type mobileSyncResponse struct {
	Culture string   `xml:"Culture"`
	User    adUser   `xml:"User"`
	Action  adAction `xml:"Action"`
}

type adUser struct {
	DisplayName  string `xml:"DisplayName"`
	EMailAddress string `xml:"EMailAddress"`
}

type adAction struct {
	Settings adSettings `xml:"Settings"`
}

type adSettings struct {
	Server adServer `xml:"Server"`
}

type adServer struct {
	Type string `xml:"Type"`
	URL  string `xml:"Url"`
	Name string `xml:"Name"`
}

// serveAutodiscover answers the mobilesync Autodiscover request (MS-ASCMD
// Autodiscover): it authenticates, then returns the ActiveSync server URL the
// device should bind to. The authenticated identity is authoritative for the
// echoed e-mail address; the request body is drained but not trusted.
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
	url := "https://" + host + "/Microsoft-Server-ActiveSync"
	resp := autodiscoverResponse{
		Response: mobileSyncResponse{
			Culture: "en:us",
			User:    adUser{DisplayName: user, EMailAddress: user},
			Action:  adAction{Settings: adSettings{Server: adServer{Type: "MobileSync", URL: url, Name: url}}},
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
