package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestUICreateRoom proves the room form carries the address, display name, seating
// capacity and resource kind through to the directory and returns the refreshed
// panel.
func TestUICreateRoom(t *testing.T) {
	d := &fakeDir{authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	resp := htmxPOST(t, ts, "/admin/ui/rooms", session, csrf,
		url.Values{"email": {"boardroom@hermex.test"}, "displayname": {"Boardroom"}, "capacity": {"12"}, "kind": {"equipment"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create room status %d, want 200", resp.StatusCode)
	}
	if d.createdRoom != "boardroom@hermex.test" || d.createdRoomName != "Boardroom" || d.createdRoomCap != 12 || !d.createdRoomEquip {
		t.Errorf("created room = %q name=%q cap=%d equip=%v, want boardroom@hermex.test / Boardroom / 12 / true",
			d.createdRoom, d.createdRoomName, d.createdRoomCap, d.createdRoomEquip)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `id="rooms-panel"`) {
		t.Errorf("response is not the rooms panel fragment: %s", body)
	}
}

// TestUIDeleteRoomRejectsMailboxUser proves the rooms page deletes only resource
// mailboxes: a delete aimed at an ordinary user is refused (the directory delete is
// never called) so the page can never remove a real mailbox, while a real room is
// deleted.
func TestUIDeleteRoomRejectsMailboxUser(t *testing.T) {
	d := &fakeDir{
		authOK: true, uid: 7, roles: []directory.AdminRole{{Role: directory.AdminSystem}},
		knownUsers: map[string]directory.UserDetail{
			"alice@hermex.test":     {DisplayType: 0},               // ordinary mailbox user
			"boardroom@hermex.test": {DisplayType: displayTypeRoom}, // a room
		},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	// Deleting a mailbox user via the rooms page must be refused.
	resp := htmxPOST(t, ts, "/admin/ui/rooms/alice@hermex.test/delete", session, csrf, url.Values{})
	resp.Body.Close()
	if d.deletedUser != "" {
		t.Errorf("a mailbox user was deleted via the rooms page (deletedUser=%q), want refusal", d.deletedUser)
	}

	// Deleting a real room is allowed.
	resp2 := htmxPOST(t, ts, "/admin/ui/rooms/boardroom@hermex.test/delete", session, csrf, url.Values{})
	resp2.Body.Close()
	if d.deletedUser != "boardroom@hermex.test" {
		t.Errorf("room delete = %q, want boardroom@hermex.test", d.deletedUser)
	}
}
