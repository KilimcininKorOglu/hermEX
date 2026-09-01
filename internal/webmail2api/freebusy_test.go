package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
)

// roomAuth is an Authenticator that also lists bookable rooms, so the room-picker
// handler has a directory whose ListRooms returns results. It embeds StaticAccounts
// for the authentication methods and overrides ListRooms.
type roomAuth struct {
	directory.StaticAccounts
	rooms []directory.GALEntry
}

func (r roomAuth) ListRooms(string) ([]directory.GALEntry, error) { return r.rooms, nil }

// Compile-time guard for the exact defect: roomLister must match the concrete
// caller-scoped ListRooms, or the type assertion in handleRooms silently misses and
// the picker is always empty. A no-arg drift would fail to build here.
var (
	_ roomLister = directory.StaticAccounts{}
	_ roomLister = (*directory.SQLDirectory)(nil)
)

// TestRoomsHandlerListsRooms proves the room picker endpoint returns the directory's
// bookable rooms. Before the fix the roomLister interface declared a no-arg ListRooms
// that no concrete directory satisfied, so the handler always returned an empty list.
func TestRoomsHandlerListsRooms(t *testing.T) {
	auth := roomAuth{
		StaticAccounts: directory.StaticAccounts{"alice@hermex.test": {}},
		rooms: []directory.GALEntry{
			{Address: "boardroom@hermex.test", DisplayName: "Boardroom", Capacity: 12},
		},
	}
	secret := []byte("rooms-test-secret")
	srv := NewServer(auth, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)

	token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: t.TempDir(), Exp: time.Now().Add(time.Hour).Unix()})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/rooms", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("rooms status = %d, want 200", rec.Code)
	}
	var out struct {
		Rooms []roomJSON `json:"rooms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Rooms) != 1 || out.Rooms[0].Email != "boardroom@hermex.test" || out.Rooms[0].Capacity != 12 {
		t.Fatalf("rooms = %+v, want the one boardroom", out.Rooms)
	}
}
