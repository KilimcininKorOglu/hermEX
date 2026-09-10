package admin

import (
	"net/http"
	"strings"

	"hermex/internal/directory"
)

// gatewayView is one gateway's form state. The password is never carried: it is write-only
// on this surface, so a stored credential cannot be read back out of the panel.
type gatewayView struct {
	Enabled     bool
	Host        string
	Port        int
	Encryption  string
	Username    string
	HasPassword bool
}

// gatewayViewOf renders a stored gateway for the form, defaulting an unconfigured one to a
// submission port with a required TLS upgrade rather than to a cleartext relay.
func gatewayViewOf(g directory.SMTPGateway, found bool) gatewayView {
	if !found {
		return gatewayView{Port: 587, Encryption: directory.GatewaySTARTTLS}
	}
	return gatewayView{
		Enabled: g.Enabled, Host: g.Host, Port: g.Port, Encryption: g.Encryption,
		Username: g.Username, HasPassword: g.Password != "",
	}
}

// addGatewaySettings merges the global gateway's form state into a page's data.
func (s *Server) addGatewaySettings(data map[string]any) {
	g, found, err := s.dir.GetSMTPGateway(directory.GlobalGateway)
	data["Gateway"] = gatewayViewOf(g, found && err == nil)
}

// gatewayFromForm reads a gateway out of a submitted form. An empty password field keeps
// the stored one, so an operator can edit the host without re-typing the credential; the
// caller supplies what is stored today.
func gatewayFromForm(r *http.Request, stored string) directory.SMTPGateway {
	password := r.FormValue("password")
	if password == "" {
		password = stored
	}
	return directory.SMTPGateway{
		Enabled:    r.FormValue("enabled") == "1",
		Host:       strings.TrimSpace(r.FormValue("host")),
		Port:       formInt(r, "port"),
		Encryption: r.FormValue("encryption"),
		Username:   strings.TrimSpace(r.FormValue("username")),
		Password:   password,
	}
}

// handleUISaveGateway persists the global outbound gateway: the server every domain's
// outgoing mail is handed to instead of resolving each recipient's mail exchangers. The MTA
// applies the change within about a minute, no restart.
func (s *Server) handleUISaveGateway(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	stored, _, _ := s.dir.GetSMTPGateway(directory.GlobalGateway)
	if err := s.dir.SetSMTPGateway(directory.GlobalGateway, gatewayFromForm(r, stored.Password)); err != nil {
		s.render(w, "gateway-panel", s.antispamPageData(r, s.notice("Could not save the gateway.", err)))
		return
	}
	s.render(w, "gateway-panel", s.antispamPageData(r,
		"Outbound gateway saved, the MTA applies it within a minute, no restart."))
}

// handleUIDeleteGateway removes the global gateway, returning every domain that has no
// gateway of its own to direct mail-exchanger delivery.
func (s *Server) handleUIDeleteGateway(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	if _, err := s.dir.DeleteSMTPGateway(directory.GlobalGateway); err != nil {
		s.render(w, "gateway-panel", s.antispamPageData(r, s.notice("Could not remove the gateway.", err)))
		return
	}
	s.render(w, "gateway-panel", s.antispamPageData(r,
		"Outbound gateway removed, mail is delivered directly to each recipient's mail exchangers again."))
}

// domainGatewayPanel re-renders one domain's gateway fragment with a notice.
func (s *Server) domainGatewayPanel(w http.ResponseWriter, r *http.Request, dd directory.DomainDetail, notice string) {
	g, found, err := s.dir.GetSMTPGateway(dd.Name)
	s.render(w, "domain-gateway-panel", map[string]any{
		"Domain": dd, "CSRF": csrfCookieValue(r), "GatewayNotice": notice,
		"Gateway": gatewayViewOf(g, found && err == nil), "GatewayOverride": found && err == nil,
	})
}

// handleUISaveDomainGateway persists one domain's gateway override, used for mail whose
// envelope sender is in that domain.
func (s *Server) handleUISaveDomainGateway(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	dd, ok := s.dkimDomain(w, r)
	if !ok {
		return
	}
	stored, _, _ := s.dir.GetSMTPGateway(dd.Name)
	if err := s.dir.SetSMTPGateway(dd.Name, gatewayFromForm(r, stored.Password)); err != nil {
		s.domainGatewayPanel(w, r, dd, s.notice("Could not save the gateway.", err))
		return
	}
	s.domainGatewayPanel(w, r, dd, "Gateway saved for this domain, the MTA applies it within a minute.")
}

// handleUIDeleteDomainGateway removes one domain's override so it follows the global
// gateway again.
func (s *Server) handleUIDeleteDomainGateway(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	dd, ok := s.dkimDomain(w, r)
	if !ok {
		return
	}
	if _, err := s.dir.DeleteSMTPGateway(dd.Name); err != nil {
		s.domainGatewayPanel(w, r, dd, s.notice("Could not remove the override.", err))
		return
	}
	s.domainGatewayPanel(w, r, dd, "Override removed, this domain follows the global gateway again.")
}
