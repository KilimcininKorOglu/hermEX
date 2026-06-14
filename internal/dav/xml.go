package dav

import "encoding/xml"

// WebDAV / CardDAV XML namespaces (RFC 4918 / RFC 6352; the calendarserver
// namespace carries the widely-implemented getctag extension).
const (
	nsDAV    = "DAV:"
	nsCard   = "urn:ietf:params:xml:ns:carddav"
	nsCalDAV = "urn:ietf:params:xml:ns:caldav"
	nsCS     = "http://calendarserver.org/ns/"
)

// multistatus is a WebDAV 207 Multistatus response body (RFC 4918 §14.16).
type multistatus struct {
	XMLName   xml.Name     `xml:"DAV: multistatus"`
	Responses []msResponse `xml:"DAV: response"`
	// SyncToken is set on a sync-collection REPORT response.
	SyncToken string `xml:"DAV: sync-token,omitempty"`
}

// msResponse is one resource's status within a multistatus.
type msResponse struct {
	Href     string       `xml:"DAV: href"`
	Propstat []msPropstat `xml:"DAV: propstat,omitempty"`
	// Status is set instead of Propstat for a whole-resource status (e.g. a
	// 404 Not Found member reported by sync-collection).
	Status string `xml:"DAV: status,omitempty"`
}

// msPropstat groups a set of properties with the status that applies to them.
type msPropstat struct {
	Prop   msProp `xml:"DAV: prop"`
	Status string `xml:"DAV: status"`
}

// msProp is the property set carried for a resource. Empty fields are omitted so
// one struct serves principals, collections, and objects.
type msProp struct {
	ResourceType       *resourceType  `xml:"DAV: resourcetype,omitempty"`
	DisplayName        string         `xml:"DAV: displayname,omitempty"`
	GetETag            string         `xml:"DAV: getetag,omitempty"`
	GetContentType     string         `xml:"DAV: getcontenttype,omitempty"`
	GetCTag            string         `xml:"http://calendarserver.org/ns/ getctag,omitempty"`
	SyncToken          string         `xml:"DAV: sync-token,omitempty"`
	CurrentUserPrOne   *href          `xml:"DAV: current-user-principal,omitempty"`
	PrincipalURL       *href          `xml:"DAV: principal-URL,omitempty"`
	AddressbookHomeSet *href          `xml:"urn:ietf:params:xml:ns:carddav addressbook-home-set,omitempty"`
	AddressData        string         `xml:"urn:ietf:params:xml:ns:carddav address-data,omitempty"`
	CalendarHomeSet    *href          `xml:"urn:ietf:params:xml:ns:caldav calendar-home-set,omitempty"`
	CalendarData       string         `xml:"urn:ietf:params:xml:ns:caldav calendar-data,omitempty"`
	SupportedCalComp   *supportedComp `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-component-set,omitempty"`
	SupportedReportSet *struct{}      `xml:"DAV: supported-report-set,omitempty"`
}

// supportedComp is the CalDAV supported-calendar-component-set value: the list of
// iCalendar component types a calendar collection accepts (hermEX v1: VEVENT).
type supportedComp struct {
	Comps []calComp `xml:"urn:ietf:params:xml:ns:caldav comp"`
}

// calComp is one <C:comp name="VEVENT"/> entry.
type calComp struct {
	Name string `xml:"name,attr"`
}

// resourceType is the DAV:resourcetype value; set members mark a collection,
// an address book, a calendar, or a principal.
type resourceType struct {
	Collection  *struct{} `xml:"DAV: collection,omitempty"`
	AddressBook *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook,omitempty"`
	Calendar    *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar,omitempty"`
	Principal   *struct{} `xml:"DAV: principal,omitempty"`
}

// href wraps a DAV:href child (used by current-user-principal,
// addressbook-home-set, etc.).
type href struct {
	Href string `xml:"DAV: href"`
}

const (
	statusOK       = "HTTP/1.1 200 OK"
	statusNotFound = "HTTP/1.1 404 Not Found"
)

var empty = &struct{}{}

// collectionResourceType marks an address-book collection.
func collectionResourceType() *resourceType {
	return &resourceType{Collection: empty, AddressBook: empty}
}

// calendarResourceType marks a calendar collection.
func calendarResourceType() *resourceType {
	return &resourceType{Collection: empty, Calendar: empty}
}

// eventComponentSet is the supported-calendar-component-set hermEX advertises: a
// calendar holds VEVENTs (v1 does not expose VTODO).
func eventComponentSet() *supportedComp {
	return &supportedComp{Comps: []calComp{{Name: "VEVENT"}}}
}

// marshalMultistatus renders a multistatus with the XML declaration prefix.
func marshalMultistatus(ms *multistatus) ([]byte, error) {
	body, err := xml.Marshal(ms)
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}
