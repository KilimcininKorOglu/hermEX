package admin

import (
	"maps"
	"net/http"
	"strconv"

	"hermex/internal/directory"
	"hermex/internal/dkimsign"
)

// dkimSelector is the DKIM selector hermEX publishes under
// {selector}._domainkey.{domain}. One selector per domain in this version;
// regenerating reuses it (the operator republishes the record value).
const dkimSelector = "hermex"

// dkimData returns a domain's DKIM panel fields: whether a key exists, its selector and
// enabled state, the TXT value to publish, and the record name. The private key is
// never included.
func (s *Server) dkimData(domain string) map[string]any {
	data := map[string]any{"DKIMHasKey": false}
	info, found, _ := s.dir.GetDKIMKeyInfo(domain)
	if found {
		data["DKIMHasKey"] = true
		data["DKIMSelector"] = info.Selector
		data["DKIMEnabled"] = info.Enabled
		data["DKIMPublicTXT"] = info.PublicTXT
		data["DKIMRecordName"] = info.Selector + "._domainkey." + domain
	}
	return data
}

// dkimPanel re-renders the DKIM panel fragment for a domain with a notice.
func (s *Server) dkimPanel(w http.ResponseWriter, r *http.Request, dd directory.DomainDetail, notice string) {
	data := map[string]any{"Domain": dd, "CSRF": csrfCookieValue(r), "DKIMNotice": notice}
	maps.Copy(data, s.dkimData(dd.Name))
	s.render(w, "dkim-panel", data)
}

// dkimDomain resolves the {domainID} path value to a domain, writing an error response
// and returning ok=false when it cannot.
func (s *Server) dkimDomain(w http.ResponseWriter, r *http.Request) (directory.DomainDetail, bool) {
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return directory.DomainDetail{}, false
	}
	dd, found, err := s.dir.GetDomain(id)
	if err != nil || !found {
		http.Error(w, "no such domain", http.StatusNotFound)
		return directory.DomainDetail{}, false
	}
	return dd, true
}

// handleUIDKIMGenerate mints a fresh signing key for the domain, stored DISABLED, and
// shows the DNS record to publish. Generating never starts signing — the operator
// publishes the record, then enables it as a separate step.
func (s *Server) handleUIDKIMGenerate(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	dd, ok := s.dkimDomain(w, r)
	if !ok {
		return
	}
	privPEM, dnsTXT, err := dkimsign.GenerateKey()
	if err != nil {
		s.dkimPanel(w, r, dd, "Could not generate a key: "+err.Error())
		return
	}
	if err := s.dir.SetDKIMKey(dd.Name, dkimSelector, privPEM, dnsTXT); err != nil {
		s.dkimPanel(w, r, dd, "Could not save the key: "+err.Error())
		return
	}
	s.dkimPanel(w, r, dd, "Key generated. Publish the DNS record below, then enable signing.")
}

// handleUIDKIMEnable turns outbound signing on or off for the domain. Enabling is a
// deliberate step taken only after the DNS record is published.
func (s *Server) handleUIDKIMEnable(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	dd, ok := s.dkimDomain(w, r)
	if !ok {
		return
	}
	enabled := r.FormValue("enabled") == "1"
	if err := s.dir.SetDKIMEnabled(dd.Name, enabled); err != nil {
		s.dkimPanel(w, r, dd, "Could not change signing: "+err.Error())
		return
	}
	verb := "disabled"
	if enabled {
		verb = "enabled"
	}
	s.dkimPanel(w, r, dd, "Signing "+verb+".")
}

// handleUIDKIMDelete removes the domain's signing key, stopping signing.
func (s *Server) handleUIDKIMDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	dd, ok := s.dkimDomain(w, r)
	if !ok {
		return
	}
	if err := s.dir.DeleteDKIMKey(dd.Name); err != nil {
		s.dkimPanel(w, r, dd, "Could not delete the key: "+err.Error())
		return
	}
	s.dkimPanel(w, r, dd, "Key deleted.")
}
