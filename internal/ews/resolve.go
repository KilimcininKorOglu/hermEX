package ews

import (
	"encoding/xml"
	"net/http"

	"hermex/internal/directory"
	"hermex/internal/oxews"
)

const resolveLimit = 100

// --- request ---

type resolveNamesRequest struct {
	UnresolvedEntry string `xml:"UnresolvedEntry"`
}

// --- response ---

type resolveNamesResponse struct {
	XMLName  xml.Name                 `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResolveNamesResponse"`
	Messages []resolveResponseMessage `xml:"ResponseMessages>ResolveNamesResponseMessage"`
}

type resolveResponseMessage struct {
	ResponseClass string         `xml:"ResponseClass,attr"`
	ResponseCode  string         `xml:"ResponseCode"`
	ResolutionSet *resolutionSet `xml:"ResolutionSet,omitempty"`
}

type resolutionSet struct {
	TotalItemsInView        int          `xml:"TotalItemsInView,attr"`
	IncludesLastItemInRange bool         `xml:"IncludesLastItemInRange,attr"`
	Resolutions             []resolution `xml:"Resolution"`
}

type resolution struct {
	Mailbox oxews.Mailbox `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox"`
}

// handleResolveNames answers ResolveNames against the directory GAL: one match is
// a Success, several a Warning (the client picks), none a Warning with no
// results — the same three-way outcome as the webmail "check names".
func (s *Server) handleResolveNames(w http.ResponseWriter, inner []byte, sess *session) {
	var req resolveNamesRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "ResolveNames: "+err.Error())
		return
	}
	gal, ok := s.accounts.(directory.GAL)
	if !ok {
		writeResolveWarning(w, "ErrorNameResolutionNoResults")
		return
	}
	entries, err := gal.SearchGAL(req.UnresolvedEntry, resolveLimit)
	if err != nil || len(entries) == 0 {
		writeResolveWarning(w, "ErrorNameResolutionNoResults")
		return
	}
	res := make([]resolution, 0, len(entries))
	for _, e := range entries {
		res = append(res, resolution{Mailbox: oxews.Mailbox{Name: e.DisplayName, EmailAddress: e.Address}})
	}
	class, code := "Success", "NoError"
	if len(entries) > 1 {
		class, code = "Warning", "ErrorNameResolutionMultipleResults"
	}
	writeResponse(w, resolveNamesResponse{Messages: []resolveResponseMessage{{
		ResponseClass: class,
		ResponseCode:  code,
		ResolutionSet: &resolutionSet{
			TotalItemsInView:        len(res),
			IncludesLastItemInRange: true,
			Resolutions:             res,
		},
	}}})
}

// writeResolveWarning writes a ResolveNames warning response (no/failed match).
func writeResolveWarning(w http.ResponseWriter, code string) {
	writeResponse(w, resolveNamesResponse{Messages: []resolveResponseMessage{{
		ResponseClass: "Warning",
		ResponseCode:  code,
	}}})
}
