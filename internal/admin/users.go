package admin

import (
	"encoding/json"
	"net/http"

	"hermex/internal/directory"
	"hermex/internal/mapi"
)

// handleListUsers lists every user. This first increment is system-admin only;
// org- and domain-scoped listing is a later refinement.
func (s *Server) handleListUsers(w http.ResponseWriter, _ *http.Request) {
	users, err := s.dir.ListUsers()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, users)
}

// handleCreateUser provisions a user (system administrators only); its maildir is
// derived from the configured data root. The domain must already exist.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Password == "" {
		http.Error(w, "an email and password are required", http.StatusBadRequest)
		return
	}
	id, err := s.dir.CreateUser(req.Email, req.Password, s.paths.MaildirFor(req.Email))
	if err != nil {
		http.Error(w, "could not create user: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": id, "email": req.Email})
}

// handleSetPassword replaces a user's local password (system administrators
// only). The user is named in the path; the new password is the request body.
func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		http.Error(w, "a password is required", http.StatusBadRequest)
		return
	}
	found, err := s.dir.SetPassword(r.PathValue("email"), req.Password)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetUser returns a single user's administrative record (system
// administrators only). The user is named in the path.
func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	u, ok, err := s.dir.GetUser(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	writeJSON(w, u)
}

// handleUpdateUser replaces the editable subset of a user's record (system
// administrators only). The whole subset is replaced, so every editable field
// must be supplied; identity fields (username, domain, maildir) are immutable.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status      int    `json:"status"`
		Lang        string `json:"lang"`
		Timezone    string `json:"timezone"`
		DisplayType int    `json:"displayType"`
		Homeserver  int    `json:"homeserver"`
		POP3IMAP    bool   `json:"pop3_imap"`
		SMTP        bool   `json:"smtp"`
		ChgPasswd   bool   `json:"chgpasswd"`
		Web         bool   `json:"web"`
		EAS         bool   `json:"eas"`
		DAV         bool   `json:"dav"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	found, err := s.dir.UpdateUser(r.PathValue("email"), directory.UserUpdate{
		Status:      req.Status,
		Lang:        req.Lang,
		Timezone:    req.Timezone,
		DisplayType: req.DisplayType,
		Homeserver:  req.Homeserver,
		POP3IMAP:    req.POP3IMAP,
		SMTP:        req.SMTP,
		ChgPasswd:   req.ChgPasswd,
		Web:         req.Web,
		EAS:         req.EAS,
		DAV:         req.DAV,
	})
	if err != nil {
		http.Error(w, "could not update user: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteUser removes a user (system administrators only). The maildir is
// removed from disk only when the deleteFiles query parameter is "true".
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	deleteFiles := r.URL.Query().Get("deleteFiles") == "true"
	found, err := s.dir.DeleteUser(r.PathValue("email"), deleteFiles)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListAltnames returns a user's alternative login names (system
// administrators only).
func (s *Server) handleListAltnames(w http.ResponseWriter, r *http.Request) {
	names, err := s.dir.ListAltnames(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, names)
}

// handleSetAltnames replaces a user's alternative login names (system
// administrators only). A name already taken by another account is rejected.
func (s *Server) handleSetAltnames(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Altnames []string `json:"altnames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	found, err := s.dir.SetAltnames(r.PathValue("email"), req.Altnames)
	if err != nil {
		http.Error(w, "could not set alternative names: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListUserAliases returns the e-mail aliases that deliver to a user (system
// administrators only).
func (s *Server) handleListUserAliases(w http.ResponseWriter, r *http.Request) {
	aliases, err := s.dir.ListAliasesFor(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if aliases == nil {
		aliases = []string{}
	}
	writeJSON(w, aliases)
}

// handleSetUserAliases replaces the e-mail aliases delivering to a user (system
// administrators only). An address already in use is rejected.
func (s *Server) handleSetUserAliases(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Aliases []string `json:"aliases"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	found, err := s.dir.SetAliasesFor(r.PathValue("email"), req.Aliases)
	if err != nil {
		http.Error(w, "could not set aliases: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// contactFields maps the user contact/detail form field names to their MAPI
// property tags in user_properties. The handlers only ever read and write these
// known tags; a raw proptag is never accepted from the client.
var contactFields = []struct {
	Field string
	Tag   uint32
}{
	{"displayname", uint32(mapi.PrDisplayName)},
	{"nickname", uint32(mapi.PrNickname)},
	{"nameprefix", uint32(mapi.PrDisplayNamePrefix)},
	{"givenname", uint32(mapi.PrGivenName)},
	{"surname", uint32(mapi.PrSurname)},
	{"title", uint32(mapi.PrTitle)},
	{"company", uint32(mapi.PrCompanyName)},
	{"department", uint32(mapi.PrDepartmentName)},
	{"office_phone", uint32(mapi.PrBusinessTelephoneNumber)},
	{"mobile_phone", uint32(mapi.PrMobileTelephoneNumber)},
	{"home_phone", uint32(mapi.PrHomeTelephoneNumber)},
	{"fax", uint32(mapi.PrBusinessFaxNumber)},
	{"pager", uint32(mapi.PrPagerTelephoneNumber)},
	{"comment", uint32(mapi.PrComment)},
}

// contactValues maps a stored proptag→value map to the named contact fields for
// rendering or JSON output.
func contactValues(props map[uint32]string) map[string]string {
	out := map[string]string{}
	for _, f := range contactFields {
		if v, ok := props[f.Tag]; ok {
			out[f.Field] = v
		}
	}
	return out
}

// contactProps maps a field-name→value map (from a JSON body or a form) to a
// proptag→value map, keeping only the known contact fields.
func contactProps(in map[string]string) map[uint32]string {
	out := map[uint32]string{}
	for _, f := range contactFields {
		if v, ok := in[f.Field]; ok {
			out[f.Tag] = v
		}
	}
	return out
}

// handleGetContact returns a user's contact/detail fields (system administrators
// only), keyed by field name.
func (s *Server) handleGetContact(w http.ResponseWriter, r *http.Request) {
	props, err := s.dir.GetUserProperties(r.PathValue("email"))
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, contactValues(props))
}

// handleSetContact writes a user's contact/detail fields (system administrators
// only). Only the known contact fields are written; an empty value clears that
// property and unknown JSON keys are ignored.
func (s *Server) handleSetContact(w http.ResponseWriter, r *http.Request) {
	var req map[string]string
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	found, err := s.dir.SetUserProperties(r.PathValue("email"), contactProps(req))
	if err != nil {
		http.Error(w, "could not set contact: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !found {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
