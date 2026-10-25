// Package assets embeds the scripted panes (and later bundled skills) so the
// anneal binary is self-contained. ANNEAL_PANE_DIR overrides pane scripts at
// runtime; full-window script UIs are loaded from ANNEAL_UI_DIR.
package assets

import "embed"

//go:embed all:ui
var UI embed.FS
