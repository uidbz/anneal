package main

import (
	"image/color"
	"math"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"github.com/uidbz/anneal/core/config"
)

func TestApplyTextScale(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	base := theme.DefaultTheme()
	want := base.Size(theme.SizeNameText)

	applyTheme(app, config.UI{TextScale: 1.5})
	got := app.Settings().Theme().Size(theme.SizeNameText)
	if math.Abs(float64(got-want*1.5)) > 0.01 {
		t.Fatalf("scaled text size = %v, want %v", got, want*1.5)
	}
	// Re-applying the same scale must not compound the wrapper.
	applyTheme(app, config.UI{TextScale: 1.5})
	if got2 := app.Settings().Theme().Size(theme.SizeNameText); math.Abs(float64(got2-got)) > 0.01 {
		t.Fatalf("scale compounded: %v -> %v", got, got2)
	}
	applyTheme(app, config.UI{TextScale: 1})
	if got := app.Settings().Theme().Size(theme.SizeNameText); got != want {
		t.Fatalf("reset size = %v, want %v", got, want)
	}
}

// allTokens is every Anneal palette name; each must resolve (and differ)
// between the light and dark variants.
var allTokens = []fyne.ThemeColorName{
	colorRail, colorSidebar, colorRaised, colorCode, colorBorder, colorMuted,
	colorOK, colorWarn, colorErr, colorIdle, colorWarnWash, colorAccentWash,
}

func TestPaletteVariants(t *testing.T) {
	th := &brandTheme{base: theme.DefaultTheme()}
	for _, name := range allTokens {
		d := th.Color(name, theme.VariantDark)
		l := th.Color(name, theme.VariantLight)
		if d == nil || l == nil {
			t.Fatalf("%s: nil color", name)
		}
		if d == l {
			t.Errorf("%s: same color in both variants", name)
		}
	}
}

func TestForcedThemeMode(t *testing.T) {
	th := &brandTheme{base: theme.DefaultTheme(), mode: "light"}
	if got := th.variant(theme.VariantDark); got != theme.VariantLight {
		t.Fatalf("forced light ignored: %v", got)
	}
	th.mode = "dark"
	if got := th.variant(theme.VariantLight); got != theme.VariantDark {
		t.Fatalf("forced dark ignored: %v", got)
	}
	th.mode = ""
	if got := th.variant(theme.VariantLight); got != theme.VariantLight {
		t.Fatalf("system mode overrode the variant: %v", got)
	}
}

// contrastRatio is the WCAG 2.1 relative-luminance ratio.
func contrastRatio(a, b color.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	lin := func(v uint32) float64 {
		s := float64(v) / 65535
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func TestPaletteContrast(t *testing.T) {
	th := &brandTheme{base: theme.DefaultTheme()}
	for _, v := range []fyne.ThemeVariant{theme.VariantDark, theme.VariantLight} {
		bg := th.Color(theme.ColorNameBackground, v)
		cases := []struct {
			name  string
			fg, c color.Color
			min   float64
		}{
			{"foreground", th.Color(theme.ColorNameForeground, v), bg, 7},
			{"muted", th.Color(colorMuted, v), bg, 4.5},
			{"accent", th.Color(theme.ColorNamePrimary, v), bg, 3},
			{"error", th.Color(colorErr, v), bg, 3},
		}
		for _, c := range cases {
			if got := contrastRatio(c.fg, c.c); got < c.min {
				t.Errorf("variant %v %s contrast %.2f < %.1f", v, c.name, got, c.min)
			}
		}
	}
}

func TestUIThemeNormalize(t *testing.T) {
	c := config.Default()
	c.UI.Theme = "sepia"
	c.UI.Theme = c.UI.ThemeMode()
	if c.UI.Theme != "system" {
		t.Fatalf("unknown theme normalized to %q", c.UI.Theme)
	}
	c.UI.Theme = "light"
	if c.UI.ThemeMode() != "light" {
		t.Fatal("light theme lost")
	}
	if got := c.UI.WithTextScale(1.25).TextScale; got != 1.25 {
		t.Fatalf("WithTextScale: %v", got)
	}
}
