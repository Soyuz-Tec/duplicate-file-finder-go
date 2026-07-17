//go:build windows

package gui

import (
	"testing"

	"github.com/Soyuz-Tec/twintidy/internal/settings"
)

func TestPlacementIntersectsVirtualScreen(t *testing.T) {
	screen := screenRectangle{x: 0, y: 0, width: 2560, height: 1440}
	cases := []struct {
		name      string
		placement settings.WindowPlacement
		visible   bool
	}{
		{"fully on screen", settings.WindowPlacement{X: 100, Y: 100, Width: 1440, Height: 860}, true},
		{"fully off to the right", settings.WindowPlacement{X: 3000, Y: 100, Width: 1440, Height: 860}, false},
		{"fully above", settings.WindowPlacement{X: 100, Y: -2000, Width: 1440, Height: 860}, false},
		{"sliver overlap below threshold", settings.WindowPlacement{X: 2550, Y: 100, Width: 1440, Height: 860}, false},
		{"detached-monitor origin still visible enough", settings.WindowPlacement{X: -1280, Y: 0, Width: 1440, Height: 860}, true},
	}
	for _, testCase := range cases {
		if got := placementIntersectsVirtualScreen(testCase.placement, screen); got != testCase.visible {
			t.Fatalf("%s: placementIntersectsVirtualScreen = %v, want %v", testCase.name, got, testCase.visible)
		}
	}

	if placementIntersectsVirtualScreen(settings.WindowPlacement{X: 0, Y: 0, Width: 800, Height: 600}, screenRectangle{}) {
		t.Fatal("an empty virtual screen must reject every placement")
	}
}

func TestInitialFolderForPickerPrefersActiveFolder(t *testing.T) {
	app := &windowsApp{operation: newOperationState()}
	app.operation.folder = `C:\active`
	app.settings.LastFolder = t.TempDir()
	if got := app.initialFolderForPicker(); got != `C:\active` {
		t.Fatalf("initialFolderForPicker = %q, want active folder", got)
	}
}

func TestInitialFolderForPickerUsesExistingLastFolder(t *testing.T) {
	app := &windowsApp{operation: newOperationState()}
	existing := t.TempDir()
	app.settings.LastFolder = existing
	if got := app.initialFolderForPicker(); got != existing {
		t.Fatalf("initialFolderForPicker = %q, want %q", got, existing)
	}
}

func TestInitialFolderForPickerDropsMissingLastFolder(t *testing.T) {
	app := &windowsApp{operation: newOperationState()}
	app.settings.LastFolder = `C:\does\not\exist\anymore-zz`
	if got := app.initialFolderForPicker(); got != "" {
		t.Fatalf("initialFolderForPicker = %q, want empty", got)
	}
}
