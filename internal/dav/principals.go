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

// expandProperty is a DAV:expand-property body (RFC 3253 §3.8): a tree of property
// elements where a property holding hrefs may request sub-properties to be expanded
// from the referenced resources in one round trip.
type expandProperty struct {
	XMLName xml.Name     `xml:"DAV: expand-property"`
	Props   []expandProp `xml:"DAV: property"`
}

type expandProp struct {
	Name  string       `xml:"name,attr"`
	Props []expandProp `xml:"DAV: property"`
}

// reportExpandProperty answers DAV:expand-property for a principal: each requested
// href-valued property (the home sets, principal URL, calendar user address) is
// returned with the requested sub-properties of the resource it points at expanded
// inline (RFC 3253 §3.8), saving the client a second round trip.
func (s *Server) reportExpandProperty(w http.ResponseWriter, r *http.Request, user string, body []byte) {
	var req expandProperty
	if err := xml.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid expand-property: "+err.Error(), http.StatusBadRequest)
		return
	}
	_, pathUser, _, _ := classify(r.URL.Path)
	if pathUser == "" {
		pathUser = user
	}
	var inner strings.Builder
	for _, p := range req.Props {
		inner.WriteString(expandPrincipalProp(pathUser, p))
	}
	resp := msResponse{
		Href:     r.URL.Path,
		Propstat: []msPropstat{{Prop: msProp{Extra: []byte(inner.String())}, Status: statusOK}},
	}
	writeMultistatus(w, &multistatus{Responses: []msResponse{resp}})
}

// expandPrincipalProp renders one requested property of a principal. A simple
// property carries its value; an href-valued one carries its hrefs, each expanded
// into a nested response when sub-properties were requested.
func expandPrincipalProp(user string, p expandProp) string {
	switch p.Name {
	case "displayname":
		return "<displayname xmlns=\"DAV:\">" + xmlEscape(user) + "</displayname>"
	case "principal-URL", "current-user-principal", "owner":
		return expandHrefProp(nsDAV, p.Name, []string{principalPath(user)}, p.Props)
	case "calendar-home-set":
		return expandHrefProp(nsCalDAV, p.Name, []string{calHomeSetPath(user)}, p.Props)
	case "addressbook-home-set":
		return expandHrefProp(nsCard, p.Name, []string{homeSetPath(user)}, p.Props)
	case "calendar-user-address-set":
		return expandHrefProp(nsCalDAV, p.Name, []string{"mailto:" + user, principalPath(user)}, p.Props)
	}
	return ""
}

// expandHrefProp renders an href-valued property element. With no requested
// sub-properties it lists the bare hrefs; otherwise each href that names a known
// resource is expanded into a nested DAV:response carrying the sub-properties.
func expandHrefProp(ns, name string, hrefs []string, sub []expandProp) string {
	var b strings.Builder
	b.WriteString("<" + name + " xmlns=\"" + ns + "\">")
	for _, h := range hrefs {
		if len(sub) == 0 {
			b.WriteString("<href xmlns=\"DAV:\">" + xmlEscape(h) + "</href>")
			continue
		}
		b.WriteString("<response xmlns=\"DAV:\"><href>" + xmlEscape(h) + "</href><propstat><prop>")
		for _, sp := range sub {
			b.WriteString(referencedSubProp(h, sp.Name))
		}
		b.WriteString("</prop><status>HTTP/1.1 200 OK</status></propstat></response>")
	}
	b.WriteString("</" + name + ">")
	return b.String()
}

// referencedSubProp renders one sub-property of a referenced resource. Only the
// stable shape of hermEX's well-known principal targets (the home sets, the
// principal itself) is known, so displayname and resourcetype are answered.
func referencedSubProp(href, name string) string {
	switch name {
	case "displayname":
		return "<displayname>" + xmlEscape(referencedDisplayName(href)) + "</displayname>"
	case "resourcetype":
		if strings.HasPrefix(href, "/dav/principals/") {
			return "<resourcetype><principal/></resourcetype>"
		}
		return "<resourcetype><collection/></resourcetype>"
	}
	return ""
}

// referencedDisplayName is the display name hermEX gives a well-known principal
// target.
func referencedDisplayName(href string) string {
	switch {
	case strings.HasPrefix(href, "/dav/calendars/"):
		return "Calendars"
	case strings.HasPrefix(href, "/dav/addressbooks/"):
		return "Address Books"
	default:
		return href
	}
}

// xmlEscape escapes the XML element-text significant characters.
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
