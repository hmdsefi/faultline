// Package ui embeds faultline's web assets: the Phase 1 run view renderer that timeline.html
// inlines, and (Phase 4) the faultline view app. It imports only the standard library.
package ui

import "embed"

// FS holds the web assets. Paths start with "static/". Phase 4 (UI) adds files but keeps this
// declaration (UI-185), and adds Static() fs.FS, which returns fs.Sub(FS, "static").
//
//go:embed static
var FS embed.FS

// Paths in FS.
const (
	TimelineTemplate = "static/timeline.html"       // single-file page template (ART-071)
	TimelineEntry    = "static/js/timeline-main.js" // entry module of timeline.html (ART-077)
	ThemeCSS         = "static/css/theme.css"       // stylesheet inlined into timeline.html
)
