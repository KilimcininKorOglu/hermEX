package admin

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/logging"
)

// handleUISaveDomainSplit stores the host that serves a split domain's addresses
// without a mailbox here, from the domain-detail form. An empty host turns the
// split off. It returns the shared save-status partial for htmx.
func (s *Server) handleUISaveDomainSplit(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.uiAuthorized(w, r)
	if !ok {
		return
	}
	data := map[string]any{}
	id, err := strconv.ParseInt(r.PathValue("domainID"), 10, 64)
	if err != nil {
		data["Error"] = "Invalid domain id."
		s.render(w, r, "user-status", data)
		return
	}
	host := strings.ToLower(strings.TrimSpace(r.PostFormValue("split_relay_host")))
	dd, found, err := s.dir.GetDomain(id)
	switch {
	case err != nil:
		data["Error"] = s.notice(domainUnread, err)
	case !found:
		data["Error"] = "No such domain."
	case host != "" && !validHostname(host):
		data["Error"] = "Enter a host name such as mail.example.com."
	case s.paths != nil && strings.EqualFold(host, s.paths.ServerHostname()):
		data["Error"] = "The split host cannot be this server."
	default:
		old, oldErr := s.dir.SplitRelayHost(dd.Name)
		if err := s.dir.SetSplitRelayHost(dd.Name, host); err != nil {
			data["Error"] = s.notice("Could not save the split domain.", err)
		} else {
			data["Saved"] = true
			s.auditSettingChange(cl.Login, "split_domain", logging.Fields{
				"domain": dd.Name, "old_host": auditOld(old, oldErr), "new_host": host,
			})
		}
	}
	s.render(w, r, "user-status", data)
}

// validHostname reports whether host is a DNS host name: dot-separated labels of
// letters, digits and inner hyphens, each 1 to 63 bytes, 253 bytes in all.
func validHostname(host string) bool {
	if len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if !validLabel(label) {
			return false
		}
	}
	return true
}

// validLabel reports whether one DNS label is well formed.
func validLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	return strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-") == ""
}
