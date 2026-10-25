// Package assets embeds the Risor UI scripts (and later bundled skills)
// so the anneal binary is self-contained. ANNEAL_UI_DIR overrides at
// runtime for live UI development.
package assets

import "embed"

//go:embed all:ui
var UI embed.FS
