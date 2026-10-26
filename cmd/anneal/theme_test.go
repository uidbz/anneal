package main

import (
	"math"
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
)

func TestApplyTextScale(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	base := app.Settings().Theme()
	want := base.Size(theme.SizeNameText)

	applyTextScale(app, base, 1.5)
	got := app.Settings().Theme().Size(theme.SizeNameText)
	if math.Abs(float64(got-want*1.5)) > 0.01 {
		t.Fatalf("scaled text size = %v, want %v", got, want*1.5)
	}
	// Re-applying the same scale must not compound the wrapper.
	applyTextScale(app, base, 1.5)
	if got2 := app.Settings().Theme().Size(theme.SizeNameText); math.Abs(float64(got2-got)) > 0.01 {
		t.Fatalf("scale compounded: %v -> %v", got, got2)
	}
	applyTextScale(app, base, 1)
	if got := app.Settings().Theme().Size(theme.SizeNameText); got != want {
		t.Fatalf("reset size = %v, want %v", got, want)
	}
}
