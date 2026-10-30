package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/uidbz/anneal/assets"
	"github.com/uidbz/anneal/core/config"
)

// Anneal's own theme color names. Widgets that need a surface (rail, sidebar,
// cards, code blocks) resolve them through theme.ColorForWidget, so a theme
// switch repaints every surface along with the stock widgets.
const (
	colorRail       fyne.ThemeColorName = "anneal.rail"       // rail + status bar
	colorSidebar    fyne.ThemeColorName = "anneal.sidebar"    // sidebar/inspector panels
	colorRaised     fyne.ThemeColorName = "anneal.raised"     // cards, composer, pills
	colorCode       fyne.ThemeColorName = "anneal.code"       // code/table blocks
	colorBorder     fyne.ThemeColorName = "anneal.border"     // hairlines, card borders
	colorMuted      fyne.ThemeColorName = "anneal.muted"      // captions, secondary text
	colorOK         fyne.ThemeColorName = "anneal.ok"         // ready/online
	colorWarn       fyne.ThemeColorName = "anneal.warn"       // busy/warning
	colorErr        fyne.ThemeColorName = "anneal.err"        // offline/error
	colorIdle       fyne.ThemeColorName = "anneal.idle"       // unknown/idle
	colorWarnWash   fyne.ThemeColorName = "anneal.warnWash"   // approval card wash
	colorAccentWash fyne.ThemeColorName = "anneal.accentWash" // selectable highlight
)

// palette is one variant's color set. Values are authored per variant so the
// light theme is a designed surface, not an inverted dark one.
type palette struct {
	background  color.Color
	foreground  color.Color
	muted       color.Color
	rail        color.Color
	sidebar     color.Color
	raised      color.Color
	code        color.Color
	border      color.Color
	button      color.Color
	disabledBtn color.Color
	input       color.Color
	inputBorder color.Color
	separator   color.Color
	placeholder color.Color
	accent      color.Color
	onAccent    color.Color
	ok          color.Color
	warn        color.Color
	err         color.Color
	idle        color.Color
	onStatus    color.Color
	hover       color.Color
	pressed     color.Color
	selection   color.Color
	focus       color.Color
	scrollBar   color.Color
	scrollBarBG color.Color
	shadow      color.Color
	overlay     color.Color
	menu        color.Color
	warnWash    color.Color
	accentWash  color.Color
}

func rgba(r, g, b, a uint8) color.NRGBA { return color.NRGBA{R: r, G: g, B: b, A: a} }

var darkPalette = palette{
	background:  rgba(0x17, 0x18, 0x1b, 0xff),
	foreground:  rgba(0xe6, 0xe7, 0xea, 0xff),
	muted:       rgba(0x9a, 0x9c, 0xa3, 0xff),
	rail:        rgba(0x11, 0x12, 0x14, 0xff),
	sidebar:     rgba(0x1c, 0x1d, 0x21, 0xff),
	raised:      rgba(0x23, 0x24, 0x29, 0xff),
	code:        rgba(0x12, 0x13, 0x16, 0xff),
	border:      rgba(0x2e, 0x30, 0x36, 0xff),
	button:      rgba(0x28, 0x29, 0x2e, 0xff),
	disabledBtn: rgba(0x1f, 0x20, 0x24, 0xff),
	input:       rgba(0x1c, 0x1d, 0x21, 0xff),
	inputBorder: rgba(0x38, 0x3a, 0x41, 0xff),
	separator:   rgba(0x2e, 0x30, 0x36, 0xff),
	placeholder: rgba(0x6f, 0x72, 0x7a, 0xff),
	accent:      rgba(0x2d, 0xd4, 0xbf, 0xff),
	onAccent:    rgba(0x06, 0x1f, 0x1c, 0xff),
	ok:          rgba(0x4a, 0xde, 0x80, 0xff),
	warn:        rgba(0xf5, 0x9e, 0x0b, 0xff),
	err:         rgba(0xef, 0x44, 0x44, 0xff),
	idle:        rgba(0x6b, 0x72, 0x80, 0xff),
	onStatus:    rgba(0x0b, 0x0d, 0x10, 0xff),
	hover:       rgba(0xff, 0xff, 0xff, 0x0d),
	pressed:     rgba(0xff, 0xff, 0xff, 0x1f),
	selection:   rgba(0x2d, 0xd4, 0xbf, 0x33),
	focus:       rgba(0x2d, 0xd4, 0xbf, 0x40),
	scrollBar:   rgba(0xff, 0xff, 0xff, 0x33),
	scrollBarBG: rgba(0x00, 0x00, 0x00, 0x00),
	shadow:      rgba(0x00, 0x00, 0x00, 0x77),
	overlay:     rgba(0x17, 0x18, 0x1b, 0xff),
	menu:        rgba(0x23, 0x24, 0x29, 0xff),
	warnWash:    rgba(0xf5, 0x9e, 0x0b, 0x1f),
	accentWash:  rgba(0x2d, 0xd4, 0xbf, 0x1f),
}

var lightPalette = palette{
	background:  rgba(0xff, 0xff, 0xff, 0xff),
	foreground:  rgba(0x1f, 0x23, 0x28, 0xff),
	muted:       rgba(0x5f, 0x66, 0x70, 0xff),
	rail:        rgba(0xee, 0xf0, 0xf2, 0xff),
	sidebar:     rgba(0xf7, 0xf8, 0xf9, 0xff),
	raised:      rgba(0xf3, 0xf4, 0xf6, 0xff),
	code:        rgba(0xf6, 0xf8, 0xfa, 0xff),
	border:      rgba(0xe2, 0xe4, 0xe8, 0xff),
	button:      rgba(0xef, 0xf1, 0xf3, 0xff),
	disabledBtn: rgba(0xf2, 0xf3, 0xf5, 0xff),
	input:       rgba(0xff, 0xff, 0xff, 0xff),
	inputBorder: rgba(0xd0, 0xd3, 0xd9, 0xff),
	separator:   rgba(0xe2, 0xe4, 0xe8, 0xff),
	placeholder: rgba(0x8a, 0x91, 0x9b, 0xff),
	accent:      rgba(0x0f, 0x76, 0x6e, 0xff),
	onAccent:    rgba(0xff, 0xff, 0xff, 0xff),
	ok:          rgba(0x15, 0x80, 0x3d, 0xff),
	warn:        rgba(0xb4, 0x53, 0x09, 0xff),
	err:         rgba(0xb9, 0x1c, 0x1c, 0xff),
	idle:        rgba(0x8a, 0x91, 0x9b, 0xff),
	onStatus:    rgba(0xff, 0xff, 0xff, 0xff),
	hover:       rgba(0x00, 0x00, 0x00, 0x0c),
	pressed:     rgba(0x00, 0x00, 0x00, 0x14),
	selection:   rgba(0x0f, 0x76, 0x6e, 0x26),
	focus:       rgba(0x0f, 0x76, 0x6e, 0x33),
	scrollBar:   rgba(0x00, 0x00, 0x00, 0x33),
	scrollBarBG: rgba(0x00, 0x00, 0x00, 0x00),
	shadow:      rgba(0x00, 0x00, 0x00, 0x33),
	overlay:     rgba(0xff, 0xff, 0xff, 0xff),
	menu:        rgba(0xff, 0xff, 0xff, 0xff),
	warnWash:    rgba(0xf5, 0x9e, 0x0b, 0x18),
	accentWash:  rgba(0x0f, 0x76, 0x6e, 0x1a),
}

func paletteFor(variant fyne.ThemeVariant) *palette {
	if variant == theme.VariantLight {
		return &lightPalette
	}
	return &darkPalette
}

// brandTheme maps the stock Fyne color names onto the Anneal palette on top
// of the app's base theme, and resolves the anneal.* names. mode forces a
// variant ("light"/"dark"); "" follows the system setting.
type brandTheme struct {
	base fyne.Theme
	mode string
}

func (t *brandTheme) variant(v fyne.ThemeVariant) fyne.ThemeVariant {
	switch t.mode {
	case "light":
		return theme.VariantLight
	case "dark":
		return theme.VariantDark
	}
	return v
}

func (t *brandTheme) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	p := paletteFor(t.variant(v))
	switch name {
	case theme.ColorNameBackground:
		return p.background
	case theme.ColorNameForeground:
		return p.foreground
	case theme.ColorNamePrimary:
		return p.accent
	case theme.ColorNameForegroundOnPrimary:
		return p.onAccent
	case theme.ColorNameError:
		return p.err
	case theme.ColorNameForegroundOnError:
		return p.onStatus
	case theme.ColorNameSuccess:
		return p.ok
	case theme.ColorNameForegroundOnSuccess:
		return p.onStatus
	case theme.ColorNameWarning:
		return p.warn
	case theme.ColorNameForegroundOnWarning:
		return p.onStatus
	case theme.ColorNameButton:
		return p.button
	case theme.ColorNameDisabled:
		return p.muted
	case theme.ColorNameDisabledButton:
		return p.disabledBtn
	case theme.ColorNameInputBackground:
		return p.input
	case theme.ColorNameInputBorder:
		return p.inputBorder
	case theme.ColorNamePlaceHolder:
		return p.placeholder
	case theme.ColorNameHover:
		return p.hover
	case theme.ColorNamePressed:
		return p.pressed
	case theme.ColorNameSelection:
		return p.selection
	case theme.ColorNameFocus:
		return p.focus
	case theme.ColorNameSeparator:
		return p.separator
	case theme.ColorNameScrollBar:
		return p.scrollBar
	case theme.ColorNameScrollBarBackground:
		return p.scrollBarBG
	case theme.ColorNameShadow:
		return p.shadow
	case theme.ColorNameOverlayBackground:
		return p.overlay
	case theme.ColorNameMenuBackground:
		return p.menu
	case theme.ColorNameHeaderBackground:
		return p.rail
	case theme.ColorNameInnerWindowBorder:
		return p.border
	case theme.ColorNameHyperlink:
		return p.accent
	case colorRail:
		return p.rail
	case colorSidebar:
		return p.sidebar
	case colorRaised:
		return p.raised
	case colorCode:
		return p.code
	case colorBorder:
		return p.border
	case colorMuted:
		return p.muted
	case colorOK:
		return p.ok
	case colorWarn:
		return p.warn
	case colorErr:
		return p.err
	case colorIdle:
		return p.idle
	case colorWarnWash:
		return p.warnWash
	case colorAccentWash:
		return p.accentWash
	}
	return t.base.Color(name, v)
}

func (t *brandTheme) Font(style fyne.TextStyle) fyne.Resource { return t.base.Font(style) }

func (t *brandTheme) Icon(name fyne.ThemeIconName) fyne.Resource { return t.base.Icon(name) }

func (t *brandTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameInputRadius:
		return 6
	case theme.SizeNameSelectionRadius:
		return 4
	case theme.SizeNameButtonRadius:
		return 6
	case theme.SizeNameCardRadius:
		return 8
	case theme.SizeNameScrollBar:
		return 8
	}
	return t.base.Size(name)
}

// textScaleTheme scales the theme's text sizes (and inline icons, so they
// track the text they sit beside) while delegating colors, fonts, icons, and
// all other sizes to the wrapped theme. Fyne widgets have no per-widget font
// size, so the theme is the only way to control text size app-wide.
type textScaleTheme struct {
	fyne.Theme
	scale float32
}

// scaledTextSizes are the theme sizes the text scale multiplies.
var scaledTextSizes = map[fyne.ThemeSizeName]bool{
	theme.SizeNameText:           true,
	theme.SizeNameCaptionText:    true,
	theme.SizeNameHeadingText:    true,
	theme.SizeNameSubHeadingText: true,
	theme.SizeNameInlineIcon:     true,
}

func (t *textScaleTheme) Size(name fyne.ThemeSizeName) float32 {
	if scaledTextSizes[name] {
		return t.Theme.Size(name) * t.scale
	}
	return t.Theme.Size(name)
}

// applyTheme installs the brand theme (base colors + light/dark mode) and the
// configured text scale. Re-applying never compounds wrappers: the theme is
// rebuilt from theme.DefaultTheme() every time; SetTheme repaints live.
func applyTheme(app fyne.App, ui config.UI) {
	var th fyne.Theme = &brandTheme{base: theme.DefaultTheme(), mode: ui.ThemeMode()}
	if scale := config.ClampTextScale(ui.TextScale); scale != 1.0 {
		th = &textScaleTheme{Theme: th, scale: float32(scale)}
	}
	app.Settings().SetTheme(th)
}

// normMode maps a theme mode string to system/light/dark ("" = system).
func normMode(mode string) string {
	switch mode {
	case "light", "dark":
		return mode
	default:
		return "system"
	}
}

// statusDot is the small colored circle that turns a status label into an
// at-a-glance chip: ok = green, warn = amber, err = red, idle = gray.
type statusDot struct {
	widget.BaseWidget
	token fyne.ThemeColorName
}

func newStatusDot() *statusDot {
	d := &statusDot{token: colorIdle}
	d.ExtendBaseWidget(d)
	return d
}

// setToken repaints the dot from a theme color name so it follows the theme.
func (d *statusDot) setToken(name fyne.ThemeColorName) {
	d.token = name
	d.Refresh()
}

func (d *statusDot) CreateRenderer() fyne.WidgetRenderer {
	return &statusDotRenderer{dot: d, circle: canvas.NewCircle(color.Transparent)}
}

type statusDotRenderer struct {
	dot    *statusDot
	circle *canvas.Circle
}

func (r *statusDotRenderer) Layout(size fyne.Size) {
	r.circle.Move(fyne.NewPos(0, 0))
	r.circle.Resize(size)
}

func (r *statusDotRenderer) MinSize() fyne.Size { return fyne.NewSize(10, 10) }

func (r *statusDotRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.circle}
}

func (r *statusDotRenderer) Refresh() {
	r.circle.FillColor = theme.Color(r.dot.token)
	r.circle.Refresh()
}

func (r *statusDotRenderer) Destroy() {}

// logoResource is the embedded brand mark for the rail.
func logoResource() fyne.Resource {
	b, err := assets.UI.ReadFile("anneal.svg")
	if err != nil {
		return theme.FyneLogo()
	}
	return fyne.NewStaticResource("anneal.svg", b)
}
