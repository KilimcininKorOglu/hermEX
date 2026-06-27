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
	ResourceType       *resourceType       `xml:"DAV: resourcetype,omitempty"`
	DisplayName        string              `xml:"DAV: displayname,omitempty"`
	GetETag            string              `xml:"DAV: getetag,omitempty"`
	GetContentType     string              `xml:"DAV: getcontenttype,omitempty"`
	GetCTag            string              `xml:"http://calendarserver.org/ns/ getctag,omitempty"`
	SyncToken          string              `xml:"DAV: sync-token,omitempty"`
	CurrentUserPrOne   *href               `xml:"DAV: current-user-principal,omitempty"`
	PrincipalURL       *href               `xml:"DAV: principal-URL,omitempty"`
	AddressbookHomeSet *href               `xml:"urn:ietf:params:xml:ns:carddav addressbook-home-set,omitempty"`
	AddressData        string              `xml:"urn:ietf:params:xml:ns:carddav address-data,omitempty"`
	CalendarHomeSet    *href               `xml:"urn:ietf:params:xml:ns:caldav calendar-home-set,omitempty"`
	CalendarData       string              `xml:"urn:ietf:params:xml:ns:caldav calendar-data,omitempty"`
	ScheduleTag        string              `xml:"urn:ietf:params:xml:ns:caldav schedule-tag,omitempty"`
	SupportedCalComp   *supportedComp      `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-component-set,omitempty"`
	SupportedReportSet *supportedReportSet `xml:"DAV: supported-report-set,omitempty"`
	// CalDAV scheduling discovery (RFC 6638 §2): the principal's calendar user
	// addresses and the URLs of its scheduling Inbox and Outbox.
	CalendarUserAddressSet *hrefSet `xml:"urn:ietf:params:xml:ns:caldav calendar-user-address-set,omitempty"`
	ScheduleInboxURL       *href    `xml:"urn:ietf:params:xml:ns:caldav schedule-inbox-URL,omitempty"`
	ScheduleOutboxURL      *href    `xml:"urn:ietf:params:xml:ns:caldav schedule-outbox-URL,omitempty"`
	// WebDAV ACL (RFC 3744): the owner principal and the privileges the current user
	// holds on the resource — the property a client reads to decide read-only vs
	// read-write.
	Owner              *href         `xml:"DAV: owner,omitempty"`
	CurrentUserPrivSet *privilegeSet `xml:"DAV: current-user-privilege-set,omitempty"`
	// WebDAV quota (RFC 4331): the mailbox's used bytes and, when a storage limit is
	// set, the bytes still available. Both are omitted when unknown; available is also
	// omitted for an unlimited mailbox.
	QuotaUsed      string `xml:"DAV: quota-used-bytes,omitempty"`
	QuotaAvailable string `xml:"DAV: quota-available-bytes,omitempty"`
	// Extra carries stored dead properties (PROPPATCH round-trip) as verbatim XML
	// elements, emitted inside <prop> after the fixed fields.
	Extra []byte `xml:",innerxml"`
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
// an address book, a calendar, a principal, or a scheduling Inbox/Outbox.
type resourceType struct {
	Collection     *struct{} `xml:"DAV: collection,omitempty"`
	AddressBook    *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook,omitempty"`
	Calendar       *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar,omitempty"`
	Principal      *struct{} `xml:"DAV: principal,omitempty"`
	ScheduleInbox  *struct{} `xml:"urn:ietf:params:xml:ns:caldav schedule-inbox,omitempty"`
	ScheduleOutbox *struct{} `xml:"urn:ietf:params:xml:ns:caldav schedule-outbox,omitempty"`
}

// href wraps a DAV:href child (used by current-user-principal,
// addressbook-home-set, etc.).
type href struct {
	Href string `xml:"DAV: href"`
}

// scheduleResponse is the body returned by a scheduling Outbox POST (RFC 6638
// §10.1): one response per recipient.
type scheduleResponse struct {
	XMLName   xml.Name           `xml:"urn:ietf:params:xml:ns:caldav schedule-response"`
	Responses []scheduleRespItem `xml:"urn:ietf:params:xml:ns:caldav response"`
}

// scheduleRespItem is one recipient's result within a schedule-response (RFC 6638
// §10.2): the recipient address, an iTIP request-status, and, on success, the
// returned calendar data (e.g. a free-busy reply).
type scheduleRespItem struct {
	Recipient     href   `xml:"urn:ietf:params:xml:ns:caldav recipient"`
	RequestStatus string `xml:"urn:ietf:params:xml:ns:caldav request-status"`
	CalendarData  string `xml:"urn:ietf:params:xml:ns:caldav calendar-data,omitempty"`
}

// hrefSet wraps several DAV:href children under one property (calendar-user-address-set).
type hrefSet struct {
	Hrefs []string `xml:"DAV: href"`
}

// privilegeSet is a DAV:current-user-privilege-set value (RFC 3744 §5.4): a flat
// list of the privileges the authenticated user holds on the resource.
type privilegeSet struct {
	Privileges []privilege `xml:"DAV: privilege"`
}

// privilege wraps a single granted privilege element inside a <D:privilege>.
type privilege struct {
	Read                        *struct{} `xml:"DAV: read,omitempty"`
	Write                       *struct{} `xml:"DAV: write,omitempty"`
	WriteProperties             *struct{} `xml:"DAV: write-properties,omitempty"`
	WriteContent                *struct{} `xml:"DAV: write-content,omitempty"`
	Bind                        *struct{} `xml:"DAV: bind,omitempty"`
	Unbind                      *struct{} `xml:"DAV: unbind,omitempty"`
	ReadACL                     *struct{} `xml:"DAV: read-acl,omitempty"`
	ReadCurrentUserPrivilegeSet *struct{} `xml:"DAV: read-current-user-privilege-set,omitempty"`
	ReadFreeBusy                *struct{} `xml:"urn:ietf:params:xml:ns:caldav read-free-busy,omitempty"`
}

// ownerPrivilegeSet is the full privilege set the mailbox owner holds on their own
// collections. hermEX mailboxes are single-owner, so the authenticated user always
// has every privilege on their own resources (RFC 3744 §5.4; CALDAV:read-free-busy
// from RFC 4791 §6.1 is aggregated under DAV:read).
func ownerPrivilegeSet() *privilegeSet {
	return &privilegeSet{Privileges: []privilege{
		{Read: empty},
		{ReadACL: empty},
		{ReadCurrentUserPrivilegeSet: empty},
		{ReadFreeBusy: empty},
		{Write: empty},
		{WriteProperties: empty},
		{WriteContent: empty},
		{Bind: empty},
		{Unbind: empty},
	}}
}

const (
	statusOK               = "HTTP/1.1 200 OK"
	statusNotFound         = "HTTP/1.1 404 Not Found"
	statusForbidden        = "HTTP/1.1 403 Forbidden"
	statusFailedDependency = "HTTP/1.1 424 Failed Dependency"
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

// scheduleInboxResourceType marks the CalDAV scheduling Inbox (RFC 6638 §2.1).
func scheduleInboxResourceType() *resourceType {
	return &resourceType{Collection: empty, ScheduleInbox: empty}
}

// scheduleOutboxResourceType marks the CalDAV scheduling Outbox (RFC 6638 §2.2).
func scheduleOutboxResourceType() *resourceType {
	return &resourceType{Collection: empty, ScheduleOutbox: empty}
}

// eventComponentSet is the supported-calendar-component-set for a calendar
// collection: it holds VEVENTs.
func eventComponentSet() *supportedComp {
	return &supportedComp{Comps: []calComp{{Name: "VEVENT"}}}
}

// todoComponentSet is the supported-calendar-component-set for the Tasks collection:
// it holds VTODOs.
func todoComponentSet() *supportedComp {
	return &supportedComp{Comps: []calComp{{Name: "VTODO"}}}
}

// supportedReportSet is the DAV:supported-report-set value: the REPORTs a resource
// accepts, so a client discovers them (RFC 3253 §3.1.5) instead of probing blind.
type supportedReportSet struct {
	Reports []supportedReport `xml:"DAV: supported-report"`
}

// supportedReport wraps one report name in <D:supported-report><D:report>….
type supportedReport struct {
	Report reportName `xml:"DAV: report"`
}

// reportName names one report element inside <D:report>. Exactly one field is set
// per entry; the rest stay nil and are omitted.
type reportName struct {
	CalendarQuery          *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar-query,omitempty"`
	CalendarMultiget       *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar-multiget,omitempty"`
	FreeBusyQuery          *struct{} `xml:"urn:ietf:params:xml:ns:caldav free-busy-query,omitempty"`
	AddressbookQuery       *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook-query,omitempty"`
	AddressbookMultiget    *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook-multiget,omitempty"`
	SyncCollection         *struct{} `xml:"DAV: sync-collection,omitempty"`
	ExpandProperty         *struct{} `xml:"DAV: expand-property,omitempty"`
	PrincipalPropSearch    *struct{} `xml:"DAV: principal-property-search,omitempty"`
	PrincipalSearchPropSet *struct{} `xml:"DAV: principal-search-property-set,omitempty"`
}

// calendarReportSet lists the REPORTs a calendar collection supports.
func calendarReportSet() *supportedReportSet {
	return &supportedReportSet{Reports: []supportedReport{
		{Report: reportName{CalendarQuery: &struct{}{}}},
		{Report: reportName{CalendarMultiget: &struct{}{}}},
		{Report: reportName{FreeBusyQuery: &struct{}{}}},
		{Report: reportName{SyncCollection: &struct{}{}}},
		{Report: reportName{ExpandProperty: &struct{}{}}},
	}}
}

// addressbookReportSet lists the REPORTs an address book collection supports.
func addressbookReportSet() *supportedReportSet {
	return &supportedReportSet{Reports: []supportedReport{
		{Report: reportName{AddressbookQuery: &struct{}{}}},
		{Report: reportName{AddressbookMultiget: &struct{}{}}},
		{Report: reportName{SyncCollection: &struct{}{}}},
		{Report: reportName{ExpandProperty: &struct{}{}}},
	}}
}

// principalReportSet lists the REPORTs a principal resource supports.
func principalReportSet() *supportedReportSet {
	return &supportedReportSet{Reports: []supportedReport{
		{Report: reportName{PrincipalPropSearch: &struct{}{}}},
		{Report: reportName{PrincipalSearchPropSet: &struct{}{}}},
		{Report: reportName{ExpandProperty: &struct{}{}}},
	}}
}

// marshalMultistatus renders a multistatus with the XML declaration prefix.
func marshalMultistatus(ms *multistatus) ([]byte, error) {
	body, err := xml.Marshal(ms)
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}
