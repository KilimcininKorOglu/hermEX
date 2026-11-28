package ews

import (
	"encoding/xml"
	"io"
	"net/http"
	"strings"

	"hermex/internal/logging"
	"hermex/internal/serve"
)

// mobileSyncSchema is the response schema an Exchange ActiveSync device names
// in AcceptableResponseSchema ([MS-ASCMD] 2.2.3.1, Autodiscover). The front
// door sends every /autodiscover/autodiscover.xml request here, so this
// handler answers in the device's schema when the device asks for it.
const mobileSyncSchema = "http://schemas.microsoft.com/exchange/autodiscover/mobilesync/responseschema/2006"

type adRequest struct {
	Request struct {
		AcceptableResponseSchema string `xml:"AcceptableResponseSchema"`
	} `xml:"Request"`
}

// wantsMobileSync reads the request body, at most maxAutodiscoverBody bytes,
// and reports whether it asks for the mobilesync response schema. A body that
// does not parse is treated as an Outlook request.
func wantsMobileSync(body io.Reader) bool {
	var req adRequest
	if err := xml.NewDecoder(io.LimitReader(body, maxAutodiscoverBody)).Decode(&req); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(req.Request.AcceptableResponseSchema), mobileSyncSchema)
}

type mobileSyncAutodiscover struct {
	XMLName  xml.Name         `xml:"http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006 Autodiscover"`
	Response mobileSyncAnswer `xml:"http://schemas.microsoft.com/exchange/autodiscover/mobilesync/responseschema/2006 Response"`
}

type mobileSyncAnswer struct {
	Culture string `xml:"Culture"`
	User    struct {
		DisplayName  string `xml:"DisplayName"`
		EMailAddress string `xml:"EMailAddress"`
	} `xml:"User"`
	Action struct {
		Settings struct {
			Server struct {
				Type string `xml:"Type"`
				URL  string `xml:"Url"`
				Name string `xml:"Name"`
			} `xml:"Server"`
		} `xml:"Settings"`
	} `xml:"Action"`
}

// writeMobileSyncAutodiscover answers with the ActiveSync endpoint for the
// authenticated user.
func (s *Server) writeMobileSyncAutodiscover(w http.ResponseWriter, r *http.Request, host, user string) {
	var resp mobileSyncAutodiscover
	resp.Response.Culture = "en:us"
	resp.Response.User.DisplayName = user
	resp.Response.User.EMailAddress = user
	srv := &resp.Response.Action.Settings.Server
	srv.Type = "MobileSync"
	srv.URL = "https://" + host + "/Microsoft-Server-ActiveSync"
	srv.Name = srv.URL
	out, err := xml.Marshal(&resp)
	if err != nil {
		s.Logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.EWS,
			Name: "autodiscover.marshal.fail", RemoteAddr: serve.ClientAddr(r), Err: err.Error()})
		http.Error(w, "an internal error occurred", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	writeReply(w, append([]byte(xml.Header), out...), s.Logger)
}
