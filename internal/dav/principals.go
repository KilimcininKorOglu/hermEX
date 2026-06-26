package dav

import (
	"encoding/xml"
	"net/http"
	"strings"

	"hermex/internal/directory"
)

// Principal-search reports (RFC 3744 §9.3-§9.5) let a client discover other calendar
// users — driving attendee and recipient lookup. They are backed by the directory's
// Global Address List; a directory that cannot enumerate users yields no matches.

// principalSearchReq is a DAV:principal-property-search body: one or more
// property-search elements, each carrying the match string to look for.
type principalSearchReq struct {
	XMLName  xml.Name         `xml:"DAV: principal-property-search"`
	Searches []propertySearch `xml:"DAV: property-search"`
}

type propertySearch struct {
	Match string `xml:"DAV: match"`
}

// reportPrincipalSearch answers DAV:principal-property-search by matching the search
// string against the GAL and returning one principal response per match (RFC 3744
// §9.4). A directory without GAL support, or an empty match, yields no principals.
func (s *Server) reportPrincipalSearch(w http.ResponseWriter, body []byte) {
	var req principalSearchReq
	if err := xml.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid principal-property-search: "+err.Error(), http.StatusBadRequest)
		return
	}
	var match string
	for _, ps := range req.Searches {
		if m := strings.TrimSpace(ps.Match); m != "" {
			match = m
			break
		}
	}

	ms := &multistatus{}
	gal, ok := s.accounts.(directory.GAL)
	if ok && match != "" {
		entries, err := gal.SearchGAL(match, 100)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, e := range entries {
			ms.Responses = append(ms.Responses, principalEntryResponse(e))
		}
	}
	writeMultistatus(w, ms)
}

// principalEntryResponse renders one matched GAL entry as a principal response with
// the properties a scheduling client needs to address the user.
func principalEntryResponse(e directory.GALEntry) msResponse {
	return msResponse{
		Href: principalPath(e.Address),
		Propstat: []msPropstat{{
			Prop: msProp{
				ResourceType:           &resourceType{Principal: empty},
				DisplayName:            e.DisplayName,
				PrincipalURL:           &href{Href: principalPath(e.Address)},
				CalendarUserAddressSet: &hrefSet{Hrefs: []string{"mailto:" + e.Address, principalPath(e.Address)}},
			},
			Status: statusOK,
		}},
	}
}

// principalSearchPropSet is the DAV:principal-search-property-set response body: the
// set of properties the principal-property-search report can match on (RFC 3744 §9.5).
type principalSearchPropSet struct {
	XMLName xml.Name              `xml:"DAV: principal-search-property-set"`
	Props   []principalSearchProp `xml:"DAV: principal-search-property"`
}

type principalSearchProp struct {
	Prop        searchableProp `xml:"DAV: prop"`
	Description string         `xml:"DAV: description"`
}

// searchableProp names a single searchable property as an empty element.
type searchableProp struct {
	DisplayName            *struct{} `xml:"DAV: displayname,omitempty"`
	CalendarUserAddressSet *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar-user-address-set,omitempty"`
}

// reportPrincipalSearchPropSet answers DAV:principal-search-property-set with the
// properties a client may search on: the display name and the calendar user address.
func reportPrincipalSearchPropSet(w http.ResponseWriter) {
	set := &principalSearchPropSet{Props: []principalSearchProp{
		{Prop: searchableProp{DisplayName: empty}, Description: "Display Name"},
		{Prop: searchableProp{CalendarUserAddressSet: empty}, Description: "Calendar User Address"},
	}}
	body, err := xml.Marshal(set)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append([]byte(xml.Header), body...))
}
