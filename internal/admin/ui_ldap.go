package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/directory"
)

// ldapFieldView is one profile field's row in the mapping form: its key, the
// standard attribute (shown as the input placeholder), and the binding's enabled
// state and attribute override.
type ldapFieldView struct {
	Key, DefaultAttr, Attr string
	Enabled                bool
}

// ldapBindingRow is one bound domain in a Directory Sync table.
type ldapBindingRow struct {
	ID         int64
	Domain     string
	PresetKey  string
	Connection string
}

// ldapPresetKeys maps a preset name to the catalogue key of its label.
var ldapPresetKeys = map[string]string{
	directory.LDAPPresetAD:            "ldap.presetAD",
	directory.LDAPPresetInetOrgPerson: "ldap.presetInetOrgPerson",
}

// presetKey returns the catalogue key naming a preset, "ldap.noPreset" for none.
func presetKey(name string) string {
	if k, ok := ldapPresetKeys[name]; ok {
		return k
	}
	return "ldap.noPreset"
}

// ldapPresetOption is one preset in a select.
type ldapPresetOption struct{ Name, Key string }

// ldapPresetOptions lists the presets in display order.
func ldapPresetOptions() []ldapPresetOption {
	names := directory.LDAPPresetNames()
	out := make([]ldapPresetOption, len(names))
	for i, n := range names {
		out[i] = ldapPresetOption{Name: n, Key: presetKey(n)}
	}
	return out
}

// bindingRows turns bindings into table rows, naming each one's connection from
// names (empty when the caller may not see connections).
func bindingRows(bindings []directory.LDAPBinding, names map[int64]string) []ldapBindingRow {
	out := make([]ldapBindingRow, len(bindings))
	for i, b := range bindings {
		out[i] = ldapBindingRow{ID: b.ID, Domain: b.Domain, PresetKey: presetKey(b.Preset), Connection: names[b.ConnectionID]}
	}
	return out
}

// handleUILDAP renders the Directory Sync overview: the connections (system
// administrators) and the bound domains the caller may read.
func (s *Server) handleUILDAP(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiClaims(r)
	if !ok {
		http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, "ldap_connections.html", s.ldapOverviewData(r, cl, ""))
}

// ldapOverviewData builds the overview page and its connections panel.
func (s *Server) ldapOverviewData(r *http.Request, cl claims, errMsg string) map[string]any {
	system := s.isSystemReadAdmin(cl.UserID)
	data := map[string]any{"Nav": "ldap", "CSRF": csrfCookieValue(r), "System": system,
		"CanWrite": s.isSystemAdmin(cl.UserID), "Error": errMsg,
		// A new connection starts with StartTLS on: an unchecked box next to an
		// ldap:// URI is a plaintext bind, which must not be the path of least resistance.
		"NewConnection": connectionView{StartTLS: true}}
	names := map[int64]string{}
	if system {
		conns, err := s.dir.ListLDAPConnections()
		data["Connections"], data["ConnectionsError"] = conns, s.listFailure("what.ldapConnections", err)
		for _, c := range conns {
			names[c.ID] = c.Name
		}
	}
	all, err := s.dir.ListLDAPBindings(0)
	if err == nil {
		all, err = s.readableBindings(cl.UserID, all)
	}
	data["Bindings"], data["BindingsError"] = bindingRows(all, names), s.listFailure("what.ldapBindings", err)
	return data
}

// connectionFromForm reads a connection form.
func connectionFromForm(r *http.Request, id int64) directory.LDAPConnection {
	return directory.LDAPConnection{
		ID: id, Name: strings.TrimSpace(r.PostFormValue("name")), URI: strings.TrimSpace(r.PostFormValue("uri")),
		StartTLS: r.PostFormValue("starttls") != "", BindDN: r.PostFormValue("bind_dn"),
		BindPassword: r.PostFormValue("bind_password"), BaseDN: r.PostFormValue("base_dn"),
		UsernameAttr: strings.TrimSpace(r.PostFormValue("username_attr")),
	}
}

// connectionWriteNotice is the catalogue key a refused connection write reports.
func (s *Server) connectionWriteNotice(err error) string {
	switch {
	case errors.Is(err, directory.ErrInsecureLDAP):
		return s.notice("ldap.insecure", err)
	case errors.Is(err, directory.ErrLDAPConnectionName):
		return s.notice("ldap.nameTaken", err)
	default:
		return s.notice("ldap.saveFailed", err)
	}
}

// handleUICreateLDAPConnection adds a connection and opens its page.
func (s *Server) handleUICreateLDAPConnection(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiAuthorized(w, r)
	if !ok {
		return
	}
	c := connectionFromForm(r, 0)
	if c.UsernameAttr != "" && !validAttrName(c.UsernameAttr) {
		s.render(w, r, "ldap-connections-panel", s.ldapOverviewData(r, cl, "ldap.badAttr"))
		return
	}
	id, err := s.dir.CreateLDAPConnection(c)
	if err != nil {
		s.render(w, r, "ldap-connections-panel", s.ldapOverviewData(r, cl, s.connectionWriteNotice(err)))
		return
	}
	w.Header().Set("HX-Redirect", "/admin/ui/ldap/connections/"+strconv.FormatInt(id, 10))
	w.WriteHeader(http.StatusOK)
}

// handleUILDAPConnection renders one connection's page (system administrators):
// its settings, the domains bound to it and the form that binds another.
func (s *Server) handleUILDAPConnection(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	id, ok := pathID(w, r, "connection")
	if !ok {
		return
	}
	c, found, err := s.dir.GetLDAPConnection(id)
	if err == nil && !found {
		http.Error(w, "no such connection", http.StatusNotFound)
		return
	}
	failed := readFailures{}
	cl, _ := s.uiClaims(r)
	data := map[string]any{"Nav": "ldap", "CSRF": csrfCookieValue(r), "ReadFailed": failed,
		"CanWrite": s.isSystemAdmin(cl.UserID), "ID": id, "Presets": ldapPresetOptions()}
	if s.noteRead(failed, "connection", "what.ldapConnection", err) {
		data["Connection"], data["BindPasswordSet"] = viewOfConnection(c), c.BindPassword != ""
	}
	s.addConnectionBindings(data, id, failed)
	s.render(w, r, "ldap_connection.html", data)
}

// addConnectionBindings adds a connection's bindings and the domains still free to
// bind. A failed read hides the bind form, which would offer a domain that is bound.
func (s *Server) addConnectionBindings(data map[string]any, id int64, failed readFailures) {
	all, err := s.dir.ListLDAPBindings(0)
	domains, domErr := s.dir.ListDomains()
	if !s.noteRead(failed, "bindings", "what.ldapBindings", err) ||
		!s.noteRead(failed, "bindings", "what.domains", domErr) {
		return
	}
	bound := map[int64]bool{}
	var own []directory.LDAPBinding
	for _, b := range all {
		bound[b.DomainID] = true
		if b.ConnectionID == id {
			own = append(own, b)
		}
	}
	var free []directory.DomainInfo
	for _, d := range domains {
		if !bound[d.ID] {
			free = append(free, d)
		}
	}
	data["Bindings"], data["FreeDomains"] = bindingRows(own, nil), free
}

// handleUISaveLDAPConnection stores a connection's settings. An empty bind password
// keeps the stored one, so a failed read of the stored connection stops the save.
func (s *Server) handleUISaveLDAPConnection(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	id, ok := pathID(w, r, "connection")
	if !ok {
		return
	}
	s.render(w, r, "ldap-status", map[string]any{"Notice": s.saveConnection(r, id)})
}

// saveConnection applies a connection form and returns the notice to show.
func (s *Server) saveConnection(r *http.Request, id int64) panelNotice {
	stored, found, err := s.dir.GetLDAPConnection(id)
	if err != nil {
		return s.failNotice("ldap.configUnread", err)
	}
	if !found {
		return errorNotice("ldap.noSuchConnection")
	}
	c := connectionFromForm(r, id)
	if c.UsernameAttr != "" && !validAttrName(c.UsernameAttr) {
		return errorNotice("ldap.badAttr")
	}
	if c.BindPassword == "" {
		c.BindPassword = stored.BindPassword
	}
	if _, err := s.dir.UpdateLDAPConnection(c); err != nil {
		return errorNotice(s.connectionWriteNotice(err))
	}
	return okNotice("ldap.saved")
}

// handleUIDeleteLDAPConnection removes a connection and its bindings, then returns
// to the overview.
func (s *Server) handleUIDeleteLDAPConnection(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	id, ok := pathID(w, r, "connection")
	if !ok {
		return
	}
	if _, err := s.dir.DeleteLDAPConnection(id); err != nil {
		s.render(w, r, "ldap-status", map[string]any{"Notice": s.failNotice("ldap.deleteFailed", err)})
		return
	}
	w.Header().Set("HX-Redirect", "/admin/ui/ldap")
	w.WriteHeader(http.StatusOK)
}

// handleUIBindLDAPDomain binds a domain to the connection with a preset's mapping
// and opens the new binding's page.
func (s *Server) handleUIBindLDAPDomain(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	connID, ok := pathID(w, r, "connection")
	if !ok {
		return
	}
	id, notice := s.bindDomain(r, connID)
	if notice.Text != "" {
		s.render(w, r, "ldap-status", map[string]any{"Notice": notice})
		return
	}
	w.Header().Set("HX-Redirect", "/admin/ui/ldap/bindings/"+strconv.FormatInt(id, 10))
	w.WriteHeader(http.StatusOK)
}

// bindDomain creates a binding from the bind form, returning its id or the notice
// that refused it.
func (s *Server) bindDomain(r *http.Request, connID int64) (int64, panelNotice) {
	domainID, err := strconv.ParseInt(r.PostFormValue("domain_id"), 10, 64)
	if err != nil {
		return 0, errorNotice("ldap.chooseDomain")
	}
	preset := r.PostFormValue("preset")
	mapping, ok := directory.LDAPPreset(preset)
	if preset != "" && !ok {
		return 0, errorNotice("ldap.unknownPreset")
	}
	id, err := s.dir.CreateLDAPBinding(directory.LDAPBinding{
		ConnectionID: connID, DomainID: domainID, Preset: preset, Mapping: mapping})
	if errors.Is(err, directory.ErrLDAPDomainBound) {
		return 0, errorNotice(s.notice("ldap.domainBound", err))
	}
	if err != nil {
		return 0, s.failNotice("ldap.bindFailed", err)
	}
	return id, panelNotice{}
}

// uiBindingScope gates a binding page or action: a session, a CSRF token on a
// write, and binding scope (bindingAllowed). A read without a session redirects to
// sign-in. When ok is false a response has already been written.
func (s *Server) uiBindingScope(w http.ResponseWriter, r *http.Request, write bool) (claims, directory.LDAPBinding, bool) {
	cl, ok := s.uiClaims(r)
	if !ok {
		if write {
			http.Error(w, "session expired", http.StatusUnauthorized)
		} else {
			http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
		}
		return cl, directory.LDAPBinding{}, false
	}
	if write && !validCSRF(r) {
		http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
		return cl, directory.LDAPBinding{}, false
	}
	b, ok := s.authorizedBinding(w, r, cl, write)
	return cl, b, ok
}

// authorizedBinding loads the {id} binding and checks the caller's scope over it.
func (s *Server) authorizedBinding(w http.ResponseWriter, r *http.Request, cl claims, write bool) (directory.LDAPBinding, bool) {
	id, ok := pathID(w, r, "binding")
	if !ok {
		return directory.LDAPBinding{}, false
	}
	b, found, err := s.dir.GetLDAPBinding(id)
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return b, false
	}
	if !found {
		http.Error(w, "no such binding", http.StatusNotFound)
		return b, false
	}
	allowed, err := s.bindingAllowed(cl.UserID, b, write)
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return b, false
	}
	if !allowed {
		http.Error(w, "forbidden: requires an administrator of this domain", http.StatusForbidden)
		return b, false
	}
	return b, true
}

// handleUILDAPBinding renders one binding's mapping page.
func (s *Server) handleUILDAPBinding(w http.ResponseWriter, r *http.Request) {
	cl, b, ok := s.uiBindingScope(w, r, false)
	if !ok {
		return
	}
	canWrite, err := s.bindingAllowed(cl.UserID, b, true)
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return
	}
	system := s.isSystemAdmin(cl.UserID)
	failed := readFailures{}
	data := map[string]any{"Nav": "ldap", "CSRF": csrfCookieValue(r), "ReadFailed": failed,
		"Binding": b, "PresetKey": presetKey(b.Preset), "Fields": fieldViews(b.Mapping),
		"CanWrite": canWrite, "System": system}
	if system {
		conns, err := s.dir.ListLDAPConnections()
		if s.noteRead(failed, "mapping", "what.ldapConnections", err) {
			data["Connections"] = conns
		}
	}
	s.render(w, r, "ldap_binding.html", data)
}

// fieldViews lists every syncable profile field with the mapping's setting for it.
func fieldViews(m directory.LDAPMapping) []ldapFieldView {
	fields := directory.LDAPProfileFields()
	out := make([]ldapFieldView, len(fields))
	for i, f := range fields {
		sf := m.Fields[f.Key]
		out[i] = ldapFieldView{Key: f.Key, DefaultAttr: f.DefaultAttr, Attr: sf.Attr, Enabled: sf.Enabled}
	}
	return out
}

// mappingFromForm reads the mapping form. The system-only inputs are read too; the
// caller discards them for anyone but a system administrator.
func mappingFromForm(r *http.Request) directory.LDAPMapping {
	m := directory.LDAPMapping{Fields: map[string]directory.LDAPSyncField{}}
	for _, f := range directory.LDAPProfileFields() {
		enabled := r.PostFormValue("field_"+f.Key+"_enabled") != ""
		attr := strings.TrimSpace(r.PostFormValue("field_" + f.Key + "_attr"))
		if enabled || attr != "" {
			m.Fields[f.Key] = directory.LDAPSyncField{Enabled: enabled, Attr: attr}
		}
	}
	m.AliasAttr = strings.TrimSpace(r.PostFormValue("alias_attr"))
	m.SyncGroups = r.PostFormValue("syncgroups") != ""
	m.SyncContacts = r.PostFormValue("synccontacts") != ""
	m.BaseDN = strings.TrimSpace(r.PostFormValue("base_dn"))
	m.GroupBaseDN = strings.TrimSpace(r.PostFormValue("group_base_dn"))
	m.GroupFilter = strings.TrimSpace(r.PostFormValue("group_filter"))
	m.ContactBaseDN = strings.TrimSpace(r.PostFormValue("contact_base_dn"))
	m.ContactFilter = strings.TrimSpace(r.PostFormValue("contact_filter"))
	return m
}

// handleUISaveLDAPBinding stores a binding's mapping. Only a system administrator
// changes the search bases, the filters and the connection.
func (s *Server) handleUISaveLDAPBinding(w http.ResponseWriter, r *http.Request) {
	cl, b, ok := s.uiBindingScope(w, r, true)
	if !ok {
		return
	}
	m := mappingFromForm(r)
	if !validMapping(m) {
		s.render(w, r, "ldap-status", map[string]any{"Notice": errorNotice("ldap.badAttr")})
		return
	}
	if s.isSystemAdmin(cl.UserID) {
		if connID, err := strconv.ParseInt(r.PostFormValue("connection_id"), 10, 64); err == nil && connID != 0 {
			b.ConnectionID = connID
		}
	} else {
		m = keepSystemFields(m, b.Mapping)
	}
	b.Mapping = m
	notice := okNotice("ldap.saved")
	if _, err := s.dir.UpdateLDAPBinding(b); err != nil {
		notice = s.failNotice("ldap.saveFailed", err)
	}
	s.render(w, r, "ldap-status", map[string]any{"Notice": notice})
}

// handleUIResetLDAPBinding restores a binding's mapping to its preset, keeping the
// system-only settings, and reloads the page so the form shows the restored values.
func (s *Server) handleUIResetLDAPBinding(w http.ResponseWriter, r *http.Request) {
	_, b, ok := s.uiBindingScope(w, r, true)
	if !ok {
		return
	}
	m, found := directory.LDAPPreset(b.Preset)
	if !found {
		s.render(w, r, "ldap-status", map[string]any{"Notice": errorNotice("ldap.noPresetToReset")})
		return
	}
	b.Mapping = keepSystemFields(m, b.Mapping)
	if _, err := s.dir.UpdateLDAPBinding(b); err != nil {
		s.render(w, r, "ldap-status", map[string]any{"Notice": s.failNotice("ldap.saveFailed", err)})
		return
	}
	w.Header().Set("HX-Redirect", "/admin/ui/ldap/bindings/"+strconv.FormatInt(b.ID, 10))
	w.WriteHeader(http.StatusOK)
}

// handleUISyncLDAPBinding queues a downsync of the binding's domain. A sync can be
// long-running, so it runs on the task worker; its result appears on the Task queue.
func (s *Server) handleUISyncLDAPBinding(w http.ResponseWriter, r *http.Request) {
	cl, b, ok := s.uiBindingScope(w, r, true)
	if !ok {
		return
	}
	notice := errorNotice("ldap.unavailable")
	if s.syncer != nil {
		id, err := s.dir.CreateTask("ldapsync", strconv.FormatInt(b.ID, 10), cl.Login)
		notice = okNotice(msg("ldap.queued", strconv.FormatInt(id, 10)))
		if err != nil {
			notice = s.failNotice("ldap.queueFailed", err)
		}
	}
	s.render(w, r, "ldap-status", map[string]any{"Notice": notice})
}

// handleUIUnbindLDAPDomain removes a binding (system administrators) and returns to
// its connection's page.
func (s *Server) handleUIUnbindLDAPDomain(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	id, ok := pathID(w, r, "binding")
	if !ok {
		return
	}
	b, found, err := s.dir.GetLDAPBinding(id)
	if err == nil && !found {
		http.Error(w, "no such binding", http.StatusNotFound)
		return
	}
	if err == nil {
		_, err = s.dir.DeleteLDAPBinding(id)
	}
	if err != nil {
		s.render(w, r, "ldap-status", map[string]any{"Notice": s.failNotice("ldap.deleteFailed", err)})
		return
	}
	w.Header().Set("HX-Redirect", "/admin/ui/ldap/connections/"+strconv.FormatInt(b.ConnectionID, 10))
	w.WriteHeader(http.StatusOK)
}
