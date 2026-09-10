package admin

import (
	"maps"
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/directory"
	"hermex/internal/dkimsign"
)

// dkimSelector is the DKIM selector hermEX publishes under
// {selector}._domainkey.{domain} when the operator names none. One selector per domain:
// regenerating with the same selector reuses the name (the operator republishes the
// record value), and naming a new one moves the record.
const dkimSelector = "hermex"

// dkimData returns a domain's DKIM panel fields: whether a key exists, its selector and
// enabled state, the TXT value to publish, the record name, and the record rendered in
// the default output mode. The private key is never included.
func (s *Server) dkimData(domain string) map[string]any {
	data := map[string]any{"DKIMHasKey": false}
	info, found, _ := s.dir.GetDKIMKeyInfo(domain)
	if found {
		recordName := info.Selector + "._domainkey." + domain
		data["DKIMHasKey"] = true
		data["DKIMSelector"] = info.Selector
		data["DKIMEnabled"] = info.Enabled
		data["DKIMPublicTXT"] = info.PublicTXT
		data["DKIMRecordName"] = recordName
		data["DKIMOutputMode"] = dkimOutputRecord
		data["DKIMOutput"] = dkimOutput(dkimOutputRecord, recordName, info.PublicTXT)
	}
	return data
}

// dkimSelectorOf returns the selector a domain's stored key publishes under, falling back
// to the default when the domain has no key yet. The DNS health check and the prescribed
// records both read it, so they name the record the domain's own key actually uses.
func (s *Server) dkimSelectorOf(domain string) string {
	info, found, err := s.dir.GetDKIMKeyInfo(domain)
	if err != nil || !found || info.Selector == "" {
		return dkimSelector
	}
	return info.Selector
}

// dkimSelectorFrom reads the operator's selector from the generate form, falling back to
// the default when the field is empty. ok is false when the value is not a valid selector,
// in which case nothing is stored: a malformed selector would publish the key under a name
// no receiver can look up.
func dkimSelectorFrom(r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.FormValue("selector"))
	if raw == "" {
		return dkimSelector, true
	}
	return normalizeSelector(raw)
}

// normalizeSelector lower-cases a selector and checks it against the RFC 6376 grammar:
// dot-separated labels, each starting and ending with a letter or digit and holding only
// letters, digits and hyphens in between.
func normalizeSelector(raw string) (string, bool) {
	sel := strings.ToLower(raw)
	if len(sel) > 63 {
		return "", false
	}
	for label := range strings.SplitSeq(sel, ".") {
		if !validSelectorLabel(label) {
			return "", false
		}
	}
	return sel, true
}

// validSelectorLabel reports whether one dot-separated selector label is well formed: it
// starts and ends with a letter or digit, and holds only letters, digits and hyphens.
func validSelectorLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	if !alphanumeric(label[0]) || !alphanumeric(label[len(label)-1]) {
		return false
	}
	for i := 1; i < len(label)-1; i++ {
		if !alphanumeric(label[i]) && label[i] != '-' {
			return false
		}
	}
	return true
}

// alphanumeric reports whether a byte is a lower-case letter or a digit.
func alphanumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// dkimKeyTypeFrom reads the requested key algorithm from the generate form. ok is false
// for anything hermEX cannot generate, so the operator sees a refusal rather than a key of
// an algorithm they did not choose.
func dkimKeyTypeFrom(r *http.Request) (string, bool) {
	switch keyType := r.FormValue("keytype"); keyType {
	case "", dkimsign.KeyRSA:
		return dkimsign.KeyRSA, true
	case dkimsign.KeyEd25519:
		return dkimsign.KeyEd25519, true
	default:
		return "", false
	}
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
// shows the DNS record to publish. Generating never starts signing, the operator
// publishes the record, then enables it as a separate step.
func (s *Server) handleUIDKIMGenerate(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	dd, ok := s.dkimDomain(w, r)
	if !ok {
		return
	}
	selector, ok := dkimSelectorFrom(r)
	if !ok {
		s.dkimPanel(w, r, dd, "Not a valid selector. Use letters, digits and hyphens, optionally separated by dots.")
		return
	}
	keyType, ok := dkimKeyTypeFrom(r)
	if !ok {
		s.dkimPanel(w, r, dd, "Not a supported key type. Choose rsa or ed25519.")
		return
	}
	privPEM, dnsTXT, err := dkimsign.GenerateKey(keyType)
	if err != nil {
		s.dkimPanel(w, r, dd, s.notice("Could not generate a key.", err))
		return
	}
	if err := s.dir.SetDKIMKey(dd.Name, selector, privPEM, dnsTXT); err != nil {
		s.dkimPanel(w, r, dd, s.notice("Could not save the key.", err))
		return
	}
	s.dkimPanel(w, r, dd, "Key generated. Publish the DNS record below, then enable signing.")
}

// handleUIDKIMOutput re-renders the published record in the operator's chosen output mode.
// It reads the stored key metadata and writes nothing, so it is gated as a read-only page
// rather than a mutation and carries no CSRF token; the private key never enters the
// response.
func (s *Server) handleUIDKIMOutput(w http.ResponseWriter, r *http.Request) {
	if !s.uiRequireSystemPage(w, r) {
		return
	}
	dd, ok := s.dkimDomain(w, r)
	if !ok {
		return
	}
	data := s.dkimData(dd.Name)
	mode := r.URL.Query().Get("mode")
	recordName, _ := data["DKIMRecordName"].(string)
	publicTXT, _ := data["DKIMPublicTXT"].(string)
	data["DKIMOutputMode"] = mode
	data["DKIMOutput"] = dkimOutput(mode, recordName, publicTXT)
	s.render(w, "dkim-output", data)
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
		s.dkimPanel(w, r, dd, s.notice("Could not change signing.", err))
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
		s.dkimPanel(w, r, dd, s.notice("Could not delete the key.", err))
		return
	}
	s.dkimPanel(w, r, dd, "Key deleted.")
}
