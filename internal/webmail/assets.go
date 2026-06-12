// Package webmail serves the hermEX web interface. It is a direct in-process
// consumer of internal/store and internal/mime (it does not loop back through
// the IMAP/SMTP daemons), rendering server-side HTML templates with htmx for
// dynamic updates.
package webmail

import "embed"

// templateFS holds the HTML templates compiled into the binary.
//
//go:embed templates/*.html
var templateFS embed.FS

// staticFS holds the static assets (htmx, stylesheet) served under /static/.
//
//go:embed static/*
var staticFS embed.FS
