package ews

import (
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// seededRoomsEWS builds an EWS test server whose directory holds the login user
// plus one bookable room (boardroom@hermex.test), so the room finder has
// something to enumerate.
func seededRoomsEWS(t *testing.T) *httptest.Server {
	t.Helper()
	accs := directory.StaticAccounts{
		testUser:                {Password: testPass, MailboxPath: t.TempDir()},
		"boardroom@hermex.test": {Room: true, MailboxPath: t.TempDir()},
	}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test").Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getRoomListsBody() string {
	return `<GetRoomLists xmlns="` + nsMessages + `"/>`
}

func getRoomsBody(listAddr string) string {
	inner := ""
	if listAddr != "" {
		inner = `<t:EmailAddress>` + listAddr + `</t:EmailAddress>`
	}
	return `<GetRooms xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<RoomList>` + inner + `</RoomList>` +
		`</GetRooms>`
}

// The parse structs fully qualify the element namespaces: <t:Address>, <t:Room>,
// and <t:Id> and their fields MUST be in the types namespace, and the RoomLists /
// Rooms wrappers in the messages namespace, or Outlook's Room Finder will not
// read them. Go's xml matches a bare local name in any namespace, so qualifying
// the tags is what makes a wrong-namespace emission fail the test.
type rlAddr struct {
	Name        string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name"`
	Email       string `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress"`
	MailboxType string `xml:"http://schemas.microsoft.com/exchange/services/2006/types MailboxType"`
}

type parsedRoomLists struct {
	Resp struct {
		Class string `xml:"ResponseClass,attr"`
		Code  string `xml:"ResponseCode"`
		Lists *struct {
			Addrs []rlAddr `xml:"http://schemas.microsoft.com/exchange/services/2006/types Address"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RoomLists"`
	} `xml:"Body>GetRoomListsResponse"`
}

type rmRoom struct {
	ID struct {
		Name        string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Name"`
		Email       string `xml:"http://schemas.microsoft.com/exchange/services/2006/types EmailAddress"`
		MailboxType string `xml:"http://schemas.microsoft.com/exchange/services/2006/types MailboxType"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Id"`
}

type parsedRooms struct {
	Resp struct {
		Class string `xml:"ResponseClass,attr"`
		Code  string `xml:"ResponseCode"`
		Rooms *struct {
			Rooms []rmRoom `xml:"http://schemas.microsoft.com/exchange/services/2006/types Room"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Rooms"`
	} `xml:"Body>GetRoomsResponse"`
}

// TestGetRoomListsFromDirectory proves GetRoomLists emits one room-list entry per
// domain that owns a room, addressed rooms@<domain> with the PublicDL mailbox
// type, in the types namespace.
func TestGetRoomListsFromDirectory(t *testing.T) {
	ts := seededRoomsEWS(t)

	_, body := soapPost(t, ts, wrapRequest(getRoomListsBody()), true)
	var p parsedRoomLists
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse GetRoomLists: %v\n%s", err, body)
	}
	if p.Resp.Class != "Success" || p.Resp.Code != "NoError" {
		t.Fatalf("class/code = %q/%q, want Success/NoError\n%s", p.Resp.Class, p.Resp.Code, body)
	}
	if p.Resp.Lists == nil || len(p.Resp.Lists.Addrs) != 1 {
		t.Fatalf("want exactly one room list (namespaces must be types-qualified)\n%s", body)
	}
	got := p.Resp.Lists.Addrs[0]
	if got.Email != "rooms@hermex.test" {
		t.Errorf("room-list address = %q, want rooms@hermex.test", got.Email)
	}
	if got.MailboxType != "PublicDL" {
		t.Errorf("room-list mailbox type = %q, want PublicDL", got.MailboxType)
	}
	if got.Name != "hermex.test" {
		t.Errorf("room-list name = %q, want the domain hermex.test", got.Name)
	}
}

// TestGetRoomsRoundTrip proves a GetRoomLists address fed back to GetRooms returns
// the room it contains, with its mailbox identity in the types namespace and the
// Mailbox (not Room) mailbox type the reference emits on a room's Id.
func TestGetRoomsRoundTrip(t *testing.T) {
	ts := seededRoomsEWS(t)

	// The address GetRoomLists would have returned.
	_, body := soapPost(t, ts, wrapRequest(getRoomsBody("rooms@hermex.test")), true)
	var p parsedRooms
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse GetRooms: %v\n%s", err, body)
	}
	if p.Resp.Class != "Success" || p.Resp.Code != "NoError" {
		t.Fatalf("class/code = %q/%q, want Success/NoError\n%s", p.Resp.Class, p.Resp.Code, body)
	}
	if p.Resp.Rooms == nil || len(p.Resp.Rooms.Rooms) != 1 {
		t.Fatalf("want exactly one room (namespaces must be types-qualified)\n%s", body)
	}
	id := p.Resp.Rooms.Rooms[0].ID
	if id.Email != "boardroom@hermex.test" {
		t.Errorf("room address = %q, want boardroom@hermex.test", id.Email)
	}
	if id.MailboxType != "Mailbox" {
		t.Errorf("room mailbox type = %q, want Mailbox", id.MailboxType)
	}
}

// TestGetRoomsMissingRoomList proves a request without a room-list address is an
// error response, not a fabricated empty success.
func TestGetRoomsMissingRoomList(t *testing.T) {
	ts := seededRoomsEWS(t)

	_, body := soapPost(t, ts, wrapRequest(getRoomsBody("")), true)
	if !strings.Contains(body, "ErrorInvalidArgument") {
		t.Fatalf("missing room list: want ErrorInvalidArgument, got %s", body)
	}
}

// TestGetRoomsUnknownDomain proves a room list for a domain with no rooms is an
// empty success (hermEX has no cross-org access-denied path to synthesize).
func TestGetRoomsUnknownDomain(t *testing.T) {
	ts := seededRoomsEWS(t)

	_, body := soapPost(t, ts, wrapRequest(getRoomsBody("rooms@other.test")), true)
	var p parsedRooms
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse GetRooms: %v\n%s", err, body)
	}
	if p.Resp.Class != "Success" || p.Resp.Code != "NoError" {
		t.Fatalf("unknown domain not a success: %s", body)
	}
	if p.Resp.Rooms != nil && len(p.Resp.Rooms.Rooms) != 0 {
		t.Errorf("want no rooms for an empty domain, got %d", len(p.Resp.Rooms.Rooms))
	}
}

// TestGetRoomListsEmpty proves a directory with no rooms answers GetRoomLists with
// a success that omits the RoomLists element entirely.
func TestGetRoomListsEmpty(t *testing.T) {
	ts, _ := seededEWS(t) // seededEWS seeds no room

	_, body := soapPost(t, ts, wrapRequest(getRoomListsBody()), true)
	var p parsedRoomLists
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse GetRoomLists: %v\n%s", err, body)
	}
	if p.Resp.Class != "Success" || p.Resp.Code != "NoError" {
		t.Fatalf("empty room finder not a success: %s", body)
	}
	if p.Resp.Lists != nil {
		t.Errorf("want RoomLists omitted when there are no rooms, got %+v", p.Resp.Lists)
	}
}
