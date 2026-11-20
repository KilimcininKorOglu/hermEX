package directory

import (
	"errors"
	"strings"
	"testing"
)

// TestBrandingValidateAdmitsTheFormShapes takes its cases from the shapes the
// branding form asks for: an https:// logo, a #rgb or #rrggbb colour, an app name
// of up to 64 and a footer of up to 200 characters, and empty fields.
func TestBrandingValidateAdmitsTheFormShapes(t *testing.T) {
	for name, b := range map[string]DomainBranding{
		"empty":            {},
		"https logo":       {LogoURL: "https://cdn.example.com/logo.png?v=2"},
		"short colour":     {PrimaryColor: "#f0A"},
		"long colour":      {PrimaryColor: "#4f46e5"},
		"64-char app name": {AppName: strings.Repeat("ş", MaxBrandingAppName)},
		"200-char footer":  {FooterText: strings.Repeat("a", MaxBrandingFooter)},
	} {
		if err := b.Validate(); err != nil {
			t.Errorf("%s: %v, want admitted", name, err)
		}
	}
}

// TestBrandingValidateRefusesOtherValues sends what the login page must never
// serve: a logo that is not an https image location, a colour that carries CSS
// beyond a hex value, an over-long name or footer, and a line break.
func TestBrandingValidateRefusesOtherValues(t *testing.T) {
	for name, b := range map[string]DomainBranding{
		"http logo":            {LogoURL: "http://cdn.example.com/logo.png"},
		"javascript logo":      {LogoURL: "javascript:alert(1)"},
		"data logo":            {LogoURL: "data:image/png;base64,AAAA"},
		"relative logo":        {LogoURL: "/logo.png"},
		"logo with userinfo":   {LogoURL: "https://user:pw@cdn.example.com/logo.png"},
		"logo with a space":    {LogoURL: "https://cdn.example.com/a b.png"},
		"oversized logo":       {LogoURL: "https://cdn.example.com/" + strings.Repeat("a", 2048)},
		"colour with CSS":      {PrimaryColor: "red;}</style><script>x</script>"},
		"named colour":         {PrimaryColor: "red"},
		"four-digit colour":    {PrimaryColor: "#abcd"},
		"65-char app name":     {AppName: strings.Repeat("a", MaxBrandingAppName+1)},
		"201-char footer":      {FooterText: strings.Repeat("a", MaxBrandingFooter+1)},
		"line break in name":   {AppName: "Acme\r\nMail"},
		"invalid UTF-8 footer": {FooterText: "\xff"},
	} {
		if err := b.Validate(); !errors.Is(err, ErrInvalidBranding) {
			t.Errorf("%s: %v, want ErrInvalidBranding", name, err)
		}
	}
}
