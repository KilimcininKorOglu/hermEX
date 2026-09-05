package ews

import (
	"encoding/xml"
	"net/http"
)

// getAppMarketplaceURLResponse is the answer to GetAppMarketplaceUrl
// ([MS-OXWSMSHR] app management). A desktop client asks for it while
// connecting, to learn where its add-in store lives.
type getAppMarketplaceURLResponse struct {
	XMLName      xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetAppMarketplaceUrlResponse"`
	ResponseCode string   `xml:"ResponseCode"`
	AppMarketURL string   `xml:"AppMarketplaceUrl"`
}

// handleGetAppMarketplaceURL answers with an empty marketplace URL. This server
// hosts no add-in store, and an empty URL is how a server says so: the client
// then offers no add-in gallery and carries on.
//
// The alternative was the default branch's ErrorInvalidRequest fault, which a
// client reads as a broken server rather than as a server without add-ins, and
// which it may retry on every connect.
func (s *Server) handleGetAppMarketplaceURL(w http.ResponseWriter) {
	writeResponse(w, getAppMarketplaceURLResponse{ResponseCode: "NoError"})
}
