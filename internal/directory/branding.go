package directory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DomainBranding is a domain's login-page branding, served unauthenticated to the
// SPA login screen keyed by the accessed domain. Empty fields fall back to the
// global default. It is stored as the domains.branding_json JSON blob.
type DomainBranding struct {
	AppName      string `json:"app_name,omitempty"`
	LogoURL      string `json:"logo_url,omitempty"`
	PrimaryColor string `json:"primary_color,omitempty"`
	FooterText   string `json:"footer_text,omitempty"`
}

// Bounds of the branding fields. The values are served on the unauthenticated
// login page, so each is held to the shape the form asks for.
const (
	MaxBrandingAppName = 64   // characters
	MaxBrandingFooter  = 200  // characters
	maxBrandingLogoURL = 2048 // bytes
)

// ErrInvalidBranding refuses a branding value outside its shape: a logo that is
// not an https:// address, a colour that is not #rgb or #rrggbb, or an app name or
// footer that is too long or carries a control character.
var ErrInvalidBranding = errors.New("directory: invalid branding value")

var brandingColor = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// Validate reports whether every set field has its allowed shape. An empty field
// is valid: it falls back to the global default.
func (b DomainBranding) Validate() error {
	if !brandingText(b.AppName, MaxBrandingAppName) || !brandingText(b.FooterText, MaxBrandingFooter) {
		return ErrInvalidBranding
	}
	if b.PrimaryColor != "" && !brandingColor.MatchString(b.PrimaryColor) {
		return ErrInvalidBranding
	}
	if b.LogoURL != "" && !brandingLogo(b.LogoURL) {
		return ErrInvalidBranding
	}
	return nil
}

// brandingText reports whether s is valid UTF-8 of at most max characters with no
// control character.
func brandingText(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

// brandingLogo reports whether s is an absolute https:// address with a host. An
// http address would load as mixed content on the HTTPS login page, and any other
// scheme (javascript:, data:) is not an image location.
func brandingLogo(s string) bool {
	if len(s) > maxBrandingLogoURL || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// Empty reports whether no branding field is set, so an all-blank save clears the
// column back to NULL and the domain inherits the global default.
func (b DomainBranding) Empty() bool {
	return b.AppName == "" && b.LogoURL == "" && b.PrimaryColor == "" && b.FooterText == ""
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
	if err := b.Validate(); err != nil {
		return err
	}
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
