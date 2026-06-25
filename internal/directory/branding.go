package directory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// DomainBranding is a domain's login-page branding, served unauthenticated to the
// SPA login screen keyed by the accessed domain. Empty fields fall back to the
// global default. It is stored as the domains.branding_json JSON blob.
type DomainBranding struct {
	AppName      string `json:"app_name,omitempty"`
	LogoURL      string `json:"logo_url,omitempty"`
	PrimaryColor string `json:"primary_color,omitempty"`
	Tagline      string `json:"tagline,omitempty"`
	FooterText   string `json:"footer_text,omitempty"`
}

// Empty reports whether no branding field is set, so an all-blank save clears the
// column back to NULL and the domain inherits the global default.
func (b DomainBranding) Empty() bool {
	return b.AppName == "" && b.LogoURL == "" && b.PrimaryColor == "" && b.Tagline == "" && b.FooterText == ""
}

// GetDomainBranding returns a domain's stored branding and whether any is set. A
// domain with no branding (NULL column or unknown domain) yields the zero value and
// false, so the caller serves the global default.
func (d *SQLDirectory) GetDomainBranding(domain string) (DomainBranding, bool, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	var raw sql.NullString
	err := d.db.QueryRow(`SELECT branding_json FROM domains WHERE domainname = ?`, domain).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DomainBranding{}, false, nil
	}
	if err != nil {
		return DomainBranding{}, false, err
	}
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return DomainBranding{}, false, nil
	}
	var b DomainBranding
	if err := json.Unmarshal([]byte(raw.String), &b); err != nil {
		return DomainBranding{}, false, err
	}
	return b, !b.Empty(), nil
}

// SetDomainBranding stores a domain's branding, clearing the column to NULL when all
// fields are blank so the domain inherits the global default.
func (d *SQLDirectory) SetDomainBranding(domain string, b DomainBranding) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	var val any
	if !b.Empty() {
		j, err := json.Marshal(b)
		if err != nil {
			return err
		}
		val = string(j)
	}
	_, err := d.db.Exec(`UPDATE domains SET branding_json = ? WHERE domainname = ?`, val, domain)
	return err
}
