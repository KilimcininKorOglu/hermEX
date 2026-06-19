package admin

import (
	"embed"
	"html/template"
)

// templateFS holds the admin UI HTML templates compiled into the binary.
//
//go:embed templates/*.html
var templateFS embed.FS

// staticFS holds the static assets served under /admin/static/.
//
//go:embed static/*
var staticFS embed.FS

// tmpl is the parsed admin UI template set. A parse failure is a build-time bug,
// so it panics.
var tmpl = template.Must(template.ParseFS(templateFS, "templates/*.html"))
