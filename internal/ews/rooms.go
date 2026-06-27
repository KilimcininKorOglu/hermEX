package ews

import (
	"encoding/xml"
	"net/http"
	"strings"

	"hermex/internal/directory"
)

// GetRoomLists and GetRooms (MS-OXWSCDATA) back Outlook's Room Finder. A room
// list is a container that groups bookable rooms; hermEX models one room list per
// mail domain that owns at least one room, keyed by the synthetic address
// rooms@<domain>. GetRoomLists enumerates those lists; GetRooms echoes a list
// address back to the rooms it contains.
//
// The room inventory comes from the directory's RoomLister (the resource
// mailboxes with display type DT_ROOM). Equipment mailboxes are resources but not
// rooms, so the Room Finder excludes them. A directory that cannot enumerate
// resources yields an empty (but successful) room finder.
//
// hermEX has no cross-organization model, and the directory already scopes
// ListRooms to the local active domains, so a GetRooms for a domain with no
// rooms simply returns an empty list rather than synthesizing an access-denied
// path the directory cannot actually evaluate.

// emailAddr is the EmailAddressType payload (MS-OXWSCDATA): a recipient's name,
// address, routing type, and address-book object class, all in the types
// namespace. Both the room-list entries and each room's Id reuse it.
type emailAddr struct {
	Name         string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name,omitempty"`
	EmailAddress string `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress,omitempty"`
	RoutingType  string `xml:"http://schemas.microsoft.com/exchange/services/2006/types RoutingType,omitempty"`
	MailboxType  string `xml:"http://schemas.microsoft.com/exchange/services/2006/types MailboxType,omitempty"`
}

// --- GetRoomLists ---

type getRoomListsResponse struct {
	XMLName       xml.Name       `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetRoomListsResponse"`
	ResponseClass string         `xml:"ResponseClass,attr"`
	ResponseCode  string         `xml:"ResponseCode"`
	RoomLists     *roomListsWrap `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RoomLists,omitempty"`
}

// roomListsWrap is ArrayOfEmailAddressesType: each room list is an <t:Address>
// EmailAddressType element.
type roomListsWrap struct {
	Addresses []emailAddr `xml:"http://schemas.microsoft.com/exchange/services/2006/types Address"`
}

// handleGetRoomLists answers GetRoomLists, emitting one room-list entry per mail
// domain that owns at least one room. RoomLists is omitted entirely when there
// are none, matching the reference's optional element.
func (s *Server) handleGetRoomLists(w http.ResponseWriter, _ []byte, _ *session) {
	resp := getRoomListsResponse{ResponseClass: "Success", ResponseCode: "NoError"}
	rl, ok := s.accounts.(directory.RoomLister)
	if !ok {
		writeResponse(w, resp)
		return
	}
	rooms, err := rl.ListRooms()
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", "GetRoomLists: "+err.Error())
		return
	}
	seen := make(map[string]bool)
	var addrs []emailAddr
	for _, r := range rooms {
		if r.DisplayType != directory.DisplayTypeRoom {
			continue
		}
		dom := addrDomain(r.Address)
		if dom == "" || seen[dom] {
			continue
		}
		seen[dom] = true
		addrs = append(addrs, emailAddr{
			Name:         dom,
			EmailAddress: "rooms@" + dom,
			RoutingType:  "SMTP",
			MailboxType:  "PublicDL",
		})
	}
	if len(addrs) > 0 {
		resp.RoomLists = &roomListsWrap{Addresses: addrs}
	}
	writeResponse(w, resp)
}

// --- GetRooms ---

type getRoomsRequest struct {
	RoomList struct {
		EmailAddress string `xml:"EmailAddress"`
	} `xml:"RoomList"`
}

type getRoomsResponse struct {
	XMLName       xml.Name   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetRoomsResponse"`
	ResponseClass string     `xml:"ResponseClass,attr"`
	ResponseCode  string     `xml:"ResponseCode"`
	Rooms         *roomsWrap `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Rooms,omitempty"`
}

// roomsWrap is ArrayOfRoomsType: each room is a <t:Room> holding only an <t:Id>
// EmailAddressType (the room's mailbox identity).
type roomsWrap struct {
	Rooms []roomEntry `xml:"http://schemas.microsoft.com/exchange/services/2006/types Room"`
}

type roomEntry struct {
	ID emailAddr `xml:"http://schemas.microsoft.com/exchange/services/2006/types Id"`
}

// handleGetRooms answers GetRooms, returning every room in the domain named by
// the requested room-list address. A request without an address is an error
// response (the room list is mandatory); a domain with no rooms is an empty
// success.
func (s *Server) handleGetRooms(w http.ResponseWriter, inner []byte, _ *session) {
	var req getRoomsRequest
	if err := xml.Unmarshal(inner, &req); err != nil {
		writeSOAPFault(w, "ErrorInvalidRequest", "GetRooms: "+err.Error())
		return
	}
	target := addrDomain(req.RoomList.EmailAddress)
	if req.RoomList.EmailAddress == "" || target == "" {
		writeResponse(w, getRoomsResponse{ResponseClass: "Error", ResponseCode: "ErrorInvalidArgument"})
		return
	}
	resp := getRoomsResponse{ResponseClass: "Success", ResponseCode: "NoError"}
	rl, ok := s.accounts.(directory.RoomLister)
	if !ok {
		writeResponse(w, resp)
		return
	}
	rooms, err := rl.ListRooms()
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", "GetRooms: "+err.Error())
		return
	}
	var entries []roomEntry
	for _, r := range rooms {
		if r.DisplayType != directory.DisplayTypeRoom || addrDomain(r.Address) != target {
			continue
		}
		name := r.DisplayName
		if name == "" {
			name = r.Address
		}
		entries = append(entries, roomEntry{ID: emailAddr{
			Name:         name,
			EmailAddress: r.Address,
			RoutingType:  "SMTP",
			MailboxType:  "Mailbox",
		}})
	}
	if len(entries) > 0 {
		resp.Rooms = &roomsWrap{Rooms: entries}
	}
	writeResponse(w, resp)
}

// addrDomain returns the lowercased domain part of an SMTP address (or a bare
// domain), or "" when there is no domain. The room-list address rooms@<domain>
// and each room address share the domain that keys a room list.
func addrDomain(addr string) string {
	addr = strings.TrimSpace(addr)
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return strings.ToLower(addr[i+1:])
	}
	return strings.ToLower(addr)
}
