package admin

import (
	"net/http"
	"strconv"
	"strings"
)

// displayTypeRoom and displayTypeEquipment are the resource-mailbox display types
// (PR_DISPLAY_TYPE), matching the values the user-detail editor offers. A delete is
// scoped to them so the rooms page can never remove an ordinary mailbox user.
const (
	displayTypeRoom      = 7
	displayTypeEquipment = 8
)

// handleUIRooms renders the rooms and equipment management page (system
// administrators only): the resource mailboxes the calendar room picker offers.
func (s *Server) handleUIRooms(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	rooms, _ := s.dir.ListRooms()
	s.render(w, "rooms.html", map[string]any{
		"Nav": "rooms", "CSRF": csrfCookieValue(r),
		"Rooms": rooms,
	})
}

// handleUICreateRoom provisions a resource mailbox from the management form (its
// maildir is derived from the configured layout, as for a user) and returns the
// refreshed panel for htmx to swap in. The filing domain comes from the address.
func (s *Server) handleUICreateRoom(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	capacity, _ := strconv.Atoi(r.PostFormValue("capacity"))
	equipment := r.PostFormValue("kind") == "equipment"
	var errMsg string
	if email == "" {
		errMsg = "A resource address is required."
	} else if _, err := s.dir.CreateRoom(email, r.PostFormValue("displayname"), s.paths.MaildirFor(email), capacity, equipment); err != nil {
		errMsg = "Could not create resource: " + err.Error()
	}
	s.renderRoomsPanel(w, r, errMsg)
}

// handleUIDeleteRoom removes the resource named in the path and returns the
// refreshed panel for htmx to swap in. It first confirms the target is a room or
// equipment so the page can never delete a mailbox user.
func (s *Server) handleUIDeleteRoom(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	email := r.PathValue("email")
	var errMsg string
	u, found, err := s.dir.GetUser(email)
	switch {
	case err != nil:
		errMsg = "Could not delete resource: " + err.Error()
	case !found || (u.DisplayType != displayTypeRoom && u.DisplayType != displayTypeEquipment):
		errMsg = "No such room."
	default:
		if _, err := s.dir.DeleteUser(email, true); err != nil {
			errMsg = "Could not delete resource: " + err.Error()
		}
	}
	s.renderRoomsPanel(w, r, errMsg)
}

// renderRoomsPanel re-renders the rooms table fragment with the current list and an
// optional error. The CSRF token is carried so the per-row delete forms in the
// swapped-in fragment keep working.
func (s *Server) renderRoomsPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	rooms, _ := s.dir.ListRooms()
	s.render(w, "rooms-panel", map[string]any{
		"Rooms": rooms, "CSRF": csrfCookieValue(r), "Error": errMsg,
	})
}
