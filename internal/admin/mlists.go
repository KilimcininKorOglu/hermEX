package admin

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/directory"
)

// mlistView is a distribution list rendered for the admin UI: the raw type and
// privilege ints carried alongside their human labels.
type mlistView struct {
	Listname  string
	ListType  int
	ListPriv  int
	TypeLabel string
	PrivLabel string
}

// mlistTypeLabel names a list_type for display.
func mlistTypeLabel(t int) string {
	if t == 2 {
		return "Domain (all users)"
	}
	return "Normal (explicit members)"
}

// mlistPrivLabel names a list_privilege for display.
func mlistPrivLabel(p int) string {
	switch p {
	case 1:
		return "Members only"
	case 2:
		return "Same domain"
	case 3:
		return "Specified senders"
	case 4:
		return "Anyone (announce)"
	default:
		return "Anyone"
	}
}

func mlistViewOf(m directory.MListInfo) mlistView {
	return mlistView{
		Listname:  m.Listname,
		ListType:  m.ListType,
		ListPriv:  m.ListPriv,
		TypeLabel: mlistTypeLabel(m.ListType),
		PrivLabel: mlistPrivLabel(m.ListPriv),
	}
}

func mlistViewsOf(lists []directory.MListInfo) []mlistView {
	out := make([]mlistView, len(lists))
	for i, m := range lists {
		out[i] = mlistViewOf(m)
	}
	return out
}

// handleUIMLists renders the distribution-lists management page (system
// administrators only).
func (s *Server) handleUIMLists(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	lists, _ := s.dir.ListMLists()
	s.render(w, "mlists.html", map[string]any{
		"Nav": "mlists", "CSRF": csrfCookieValue(r), "Lists": mlistViewsOf(lists),
	})
}

// handleUICreateMList creates a distribution list from the management form and
// returns the refreshed panel for htmx to swap in.
func (s *Server) handleUICreateMList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	name := r.PostFormValue("listname")
	var errMsg string
	switch name {
	case "":
		errMsg = "A list address is required."
	default:
		listType, _ := strconv.Atoi(r.PostFormValue("type"))
		listPriv, _ := strconv.Atoi(r.PostFormValue("privilege"))
		if _, err := s.dir.CreateMList(name, listType, listPriv); err != nil {
			errMsg = "Could not create list: " + err.Error()
		}
	}
	lists, _ := s.dir.ListMLists()
	s.render(w, "mlists-panel", map[string]any{"Lists": mlistViewsOf(lists), "Error": errMsg})
}

// handleUIMListDetail renders a distribution list's management page: its type and
// privilege, its members, and (for the "specified" privilege) its permitted
// senders.
func (s *Server) handleUIMListDetail(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	addr := r.PathValue("addr")
	lists, _ := s.dir.ListMLists()
	var info *directory.MListInfo
	for i := range lists {
		if lists[i].Listname == addr {
			info = &lists[i]
			break
		}
	}
	if info == nil {
		http.Error(w, "no such list", http.StatusNotFound)
		return
	}
	members, _ := s.dir.ListMembers(addr)
	specifieds, _ := s.dir.ListSpecifieds(addr)
	s.render(w, "mlist_detail.html", map[string]any{
		"Nav": "mlists", "CSRF": csrfCookieValue(r),
		"List":       mlistViewOf(*info),
		"Members":    strings.Join(members, "\n"),
		"Specifieds": strings.Join(specifieds, "\n"),
		"Owner":      info.Owner,
	})
}

// handleUIMListMembers saves a list's explicit members from the form and returns
// the refreshed status panel.
func (s *Server) handleUIMListMembers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	found, err := s.dir.SetMembers(r.PathValue("addr"), strings.Fields(r.PostFormValue("members")))
	s.render(w, "user-status", mlistStatus(found, err, "members"))
}

// handleUIMListSpecifieds saves a list's permitted senders from the form and
// returns the refreshed status panel.
func (s *Server) handleUIMListSpecifieds(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	found, err := s.dir.SetSpecifieds(r.PathValue("addr"), strings.Fields(r.PostFormValue("specifieds")))
	s.render(w, "user-status", mlistStatus(found, err, "permitted senders"))
}

// handleUIMListOwner sets or clears a list's owner (the Exchange managedBy
// attribute) from the form and returns the refreshed status panel.
func (s *Server) handleUIMListOwner(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	found, err := s.dir.SetMListOwner(r.PathValue("addr"), r.PostFormValue("owner"))
	s.render(w, "user-status", mlistStatus(found, err, "owner"))
}

// handleUIDeleteMList deletes a distribution list and redirects htmx back to the
// lists page.
func (s *Server) handleUIDeleteMList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	if _, err := s.dir.DeleteMList(r.PathValue("addr")); err != nil {
		http.Error(w, "could not delete list: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("HX-Redirect", "/admin/ui/mlists")
	w.WriteHeader(http.StatusOK)
}

// mlistStatus builds the status-panel data for a list membership save.
func mlistStatus(found bool, err error, what string) map[string]any {
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Could not save " + what + ": " + err.Error()
	case !found:
		data["Error"] = "No such list."
	default:
		data["Saved"] = true
	}
	return data
}
