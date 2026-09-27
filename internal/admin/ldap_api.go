package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"hermex/internal/directory"
)

// connectionView is a connection as the API returns it: the bind password is never
// disclosed, only whether one is stored.
type connectionView struct {
	ID              int64
	Name            string
	URI             string
	StartTLS        bool
	BindDN          string
	BaseDN          string
	UsernameAttr    string
	BindPasswordSet bool
}

// viewOfConnection strips the secret from a connection.
func viewOfConnection(c directory.LDAPConnection) connectionView {
	return connectionView{ID: c.ID, Name: c.Name, URI: c.URI, StartTLS: c.StartTLS, BindDN: c.BindDN,
		BaseDN: c.BaseDN, UsernameAttr: c.UsernameAttr, BindPasswordSet: c.BindPassword != ""}
}

// connectionInput is the JSON body that creates or replaces a connection. An empty
// BindPassword on a replace keeps the stored one.
type connectionInput struct {
	Name         string
	URI          string
	StartTLS     bool
	BindDN       string
	BindPassword string
	BaseDN       string
	UsernameAttr string
}

// connection turns the input into a directory connection with the given id.
func (in connectionInput) connection(id int64) directory.LDAPConnection {
	return directory.LDAPConnection{ID: id, Name: in.Name, URI: in.URI, StartTLS: in.StartTLS,
		BindDN: in.BindDN, BindPassword: in.BindPassword, BaseDN: in.BaseDN, UsernameAttr: in.UsernameAttr}
}

// bindingInput is the JSON body that creates or edits a binding. A create with a
// Preset starts from that preset's mapping; an edit ignores Preset and DomainID.
type bindingInput struct {
	ConnectionID int64
	DomainID     int64
	Preset       string
	Mapping      directory.LDAPMapping
}

// pathID parses the {id} path value, writing a 400 when it is not a number.
func pathID(w http.ResponseWriter, r *http.Request, what string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid "+what+" id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// failConnectionWrite answers a refused connection write with a status and a fixed
// message per cause, so no directory error text reaches the client.
func (s *Server) failConnectionWrite(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, directory.ErrInsecureLDAP):
		s.fail(w, "the directory URI must be ldaps:// or StartTLS must be enabled", err, http.StatusBadRequest)
	case errors.Is(err, directory.ErrLDAPConnectionName):
		s.fail(w, "a connection needs a unique name", err, http.StatusConflict)
	default:
		s.fail(w, "could not save the connection", err, http.StatusBadRequest)
	}
}

// handleListLDAPConnections lists every connection (system read).
func (s *Server) handleListLDAPConnections(w http.ResponseWriter, _ *http.Request) {
	conns, err := s.dir.ListLDAPConnections()
	if err != nil {
		s.fail(w, "could not list connections", err, http.StatusInternalServerError)
		return
	}
	out := make([]connectionView, len(conns))
	for i, c := range conns {
		out[i] = viewOfConnection(c)
	}
	writeJSON(w, out)
}

// handleCreateLDAPConnection stores a new connection (system write).
func (s *Server) handleCreateLDAPConnection(w http.ResponseWriter, r *http.Request) {
	var in connectionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if in.UsernameAttr != "" && !validAttrName(in.UsernameAttr) {
		http.Error(w, "the username attribute is not a valid LDAP attribute name", http.StatusBadRequest)
		return
	}
	id, err := s.dir.CreateLDAPConnection(in.connection(0))
	if err != nil {
		s.failConnectionWrite(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]int64{"id": id})
}

// loadConnection reads the connection the {id} path value names. When ok is false a
// response has already been written.
func (s *Server) loadConnection(w http.ResponseWriter, r *http.Request) (directory.LDAPConnection, bool) {
	id, ok := pathID(w, r, "connection")
	if !ok {
		return directory.LDAPConnection{}, false
	}
	c, found, err := s.dir.GetLDAPConnection(id)
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return c, false
	}
	if !found {
		http.Error(w, "no such connection", http.StatusNotFound)
		return c, false
	}
	return c, true
}

// handleGetLDAPConnection returns one connection and the domains bound to it
// (system read).
func (s *Server) handleGetLDAPConnection(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadConnection(w, r)
	if !ok {
		return
	}
	bindings, err := s.dir.ListLDAPBindings(c.ID)
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, struct {
		Connection connectionView
		Bindings   []directory.LDAPBinding
	}{viewOfConnection(c), bindings})
}

// handleUpdateLDAPConnection replaces a connection (system write). An empty bind
// password keeps the stored one, so the secret need not round-trip through the
// client; the stored one is read first, and a failed read stops the write.
func (s *Server) handleUpdateLDAPConnection(w http.ResponseWriter, r *http.Request) {
	stored, ok := s.loadConnection(w, r)
	if !ok {
		return
	}
	var in connectionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if in.UsernameAttr != "" && !validAttrName(in.UsernameAttr) {
		http.Error(w, "the username attribute is not a valid LDAP attribute name", http.StatusBadRequest)
		return
	}
	c := in.connection(stored.ID)
	if c.BindPassword == "" {
		c.BindPassword = stored.BindPassword
	}
	if _, err := s.dir.UpdateLDAPConnection(c); err != nil {
		s.failConnectionWrite(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteLDAPConnection removes a connection and every binding to it (system
// write).
func (s *Server) handleDeleteLDAPConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "connection")
	if !ok {
		return
	}
	found, err := s.dir.DeleteLDAPConnection(id)
	if err != nil {
		s.fail(w, "could not delete the connection", err, http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such connection", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListLDAPBindings lists the bindings the caller may read.
func (s *Server) handleListLDAPBindings(w http.ResponseWriter, r *http.Request) {
	all, err := s.dir.ListLDAPBindings(0)
	if err != nil {
		s.fail(w, "could not list bindings", err, http.StatusInternalServerError)
		return
	}
	out, err := s.readableBindings(claimsOf(r).UserID, all)
	if err != nil {
		s.fail(w, "server error", err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

// readableBindings keeps the bindings a caller may read.
func (s *Server) readableBindings(userID int64, all []directory.LDAPBinding) ([]directory.LDAPBinding, error) {
	out := make([]directory.LDAPBinding, 0, len(all))
	for _, b := range all {
		ok, err := s.bindingAllowed(userID, b, false)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, b)
		}
	}
	return out, nil
}

// failBindingWrite answers a refused binding write with a status and a fixed message
// per cause.
func (s *Server) failBindingWrite(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, directory.ErrLDAPDomainBound):
		s.fail(w, "the domain is already bound to a connection", err, http.StatusConflict)
	case errors.Is(err, directory.ErrLDAPPreset):
		s.fail(w, "unknown preset", err, http.StatusBadRequest)
	default:
		s.fail(w, "could not save the binding", err, http.StatusBadRequest)
	}
}

// handleCreateLDAPBinding binds a domain to a connection (system write). A named
// preset supplies the starting mapping.
func (s *Server) handleCreateLDAPBinding(w http.ResponseWriter, r *http.Request) {
	var in bindingInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if in.Preset != "" {
		m, ok := directory.LDAPPreset(in.Preset)
		if !ok {
			http.Error(w, "unknown preset", http.StatusBadRequest)
			return
		}
		in.Mapping = m
	}
	if !validMapping(in.Mapping) {
		http.Error(w, badMappingMsg, http.StatusBadRequest)
		return
	}
	id, err := s.dir.CreateLDAPBinding(directory.LDAPBinding{
		ConnectionID: in.ConnectionID, DomainID: in.DomainID, Preset: in.Preset, Mapping: in.Mapping})
	if err != nil {
		s.failBindingWrite(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]int64{"id": id})
}

// handleGetLDAPBinding returns one binding (binding read scope).
func (s *Server) handleGetLDAPBinding(w http.ResponseWriter, r *http.Request) {
	b, ok := s.bindingScope(w, r)
	if !ok {
		return
	}
	writeJSON(w, b)
}

// handlePutLDAPBinding replaces a binding's mapping (binding write scope). Only a
// system administrator may move it to another connection or change its search
// bases and filters; for anyone else those keep their stored values.
func (s *Server) handlePutLDAPBinding(w http.ResponseWriter, r *http.Request) {
	b, ok := s.bindingScope(w, r)
	if !ok {
		return
	}
	var in bindingInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	if !validMapping(in.Mapping) {
		http.Error(w, badMappingMsg, http.StatusBadRequest)
		return
	}
	s.saveBinding(w, r, b, in.Mapping, in.ConnectionID)
}

// saveBinding applies an edited mapping to a stored binding, honouring the
// system-only settings, and answers 204.
func (s *Server) saveBinding(w http.ResponseWriter, r *http.Request, b directory.LDAPBinding, m directory.LDAPMapping, connID int64) {
	if !s.isSystemAdmin(claimsOf(r).UserID) {
		m, connID = keepSystemFields(m, b.Mapping), 0
	}
	if connID != 0 {
		b.ConnectionID = connID
	}
	b.Mapping = m
	if _, err := s.dir.UpdateLDAPBinding(b); err != nil {
		s.failBindingWrite(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResetLDAPBinding restores a binding's mapping to its preset (binding write
// scope). The system-only settings keep their stored values.
func (s *Server) handleResetLDAPBinding(w http.ResponseWriter, r *http.Request) {
	b, ok := s.bindingScope(w, r)
	if !ok {
		return
	}
	m, found := directory.LDAPPreset(b.Preset)
	if !found {
		http.Error(w, "the binding has no preset to restore", http.StatusBadRequest)
		return
	}
	b.Mapping = keepSystemFields(m, b.Mapping)
	if _, err := s.dir.UpdateLDAPBinding(b); err != nil {
		s.failBindingWrite(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteLDAPBinding unbinds a domain (system write).
func (s *Server) handleDeleteLDAPBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "binding")
	if !ok {
		return
	}
	found, err := s.dir.DeleteLDAPBinding(id)
	if err != nil {
		s.fail(w, "could not delete the binding", err, http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no such binding", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSyncLDAPBinding queues a downsync of one binding (binding write scope) and
// answers 202 with the task id; the result appears on the task queue.
func (s *Server) handleSyncLDAPBinding(w http.ResponseWriter, r *http.Request) {
	b, ok := s.bindingScope(w, r)
	if !ok {
		return
	}
	if s.syncer == nil {
		http.Error(w, "directory sync is not available", http.StatusServiceUnavailable)
		return
	}
	id, err := s.dir.CreateTask("ldapsync", strconv.FormatInt(b.ID, 10), claimsOf(r).Login)
	if err != nil {
		s.fail(w, "could not queue the sync", err, http.StatusInternalServerError)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]int64{"task": id})
}
