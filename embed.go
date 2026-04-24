package datapulse

import "embed"

// TemplateFS holds HTML templates for ParseFS.
//
//go:embed templates/*.html
var TemplateFS embed.FS

// StaticFS holds CSS/JS for the file server.
//
//go:embed static
var StaticFS embed.FS
