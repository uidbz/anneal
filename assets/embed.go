// Package assets embeds the Risor UI scripts (and later bundled skills)
// so the fyagent binary is self-contained. FYAGENT_UI_DIR overrides at
// runtime for live UI development.
package assets

import "embed"

//go:embed all:ui
var UI embed.FS
