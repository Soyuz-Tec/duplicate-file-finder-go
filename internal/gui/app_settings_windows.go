//go:build windows

package gui

import (
	"os"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	"github.com/Soyuz-Tec/duplicate-file-finder-go/internal/diagnostics"
	"github.com/Soyuz-Tec/duplicate-file-finder-go/internal/settings"
)

// loadPersistedSettings resolves and reads the preference file. Failures are
// logged and degrade to defaults; preferences must never block startup.
func loadPersistedSettings() (settings.Settings, string) {
	path, err := settings.DefaultPath()
	if err != nil {
		diagnostics.Logf("settings path unavailable: error_type=%T", err)
		return settings.Defaults(), ""
	}
	return settings.Load(path), path
}

// applyPersistedWindowPlacement restores the saved main-window bounds when
// the rectangle still intersects the current virtual screen, so a detached
// monitor cannot strand the window off-screen.
func (a *windowsApp) applyPersistedWindowPlacement() {
	placement := a.settings.Window
	if placement == nil || !placement.Valid() {
		return
	}
	if !placementIntersectsVirtualScreen(*placement, virtualScreenRectangle()) {
		diagnostics.Logf("persisted window placement ignored: off-screen")
		return
	}
	if err := a.mw.SetBounds(walk.Rectangle{
		X:      placement.X,
		Y:      placement.Y,
		Width:  placement.Width,
		Height: placement.Height,
	}); err != nil {
		diagnostics.Logf("persisted window placement failed: error_type=%T", err)
	}
}

// persistSettings captures the current window bounds and writes the
// preference file. It is best-effort: failure is logged, never surfaced.
func (a *windowsApp) persistSettings() {
	if a.settingsPath == "" {
		return
	}
	if a.mw != nil {
		bounds := a.mw.Bounds()
		placement := settings.WindowPlacement{
			X:      bounds.X,
			Y:      bounds.Y,
			Width:  bounds.Width,
			Height: bounds.Height,
		}
		if placement.Valid() {
			a.settings.Window = &placement
		}
	}
	if err := settings.Save(a.settingsPath, a.settings); err != nil {
		diagnostics.Logf("settings save failed: error_type=%T", err)
	}
}

// initialFolderForPicker prefers the active folder, then the persisted last
// folder when it still exists as a directory. The result seeds the folder
// dialog only; it grants no scan authority by itself.
func (a *windowsApp) initialFolderForPicker() string {
	if a.operation.folder != "" {
		return a.operation.folder
	}
	last := a.settings.LastFolder
	if last == "" {
		return ""
	}
	info, err := os.Stat(last)
	if err != nil || !info.IsDir() {
		return ""
	}
	return last
}

type screenRectangle struct {
	x, y, width, height int
}

func virtualScreenRectangle() screenRectangle {
	return screenRectangle{
		x:      int(win.GetSystemMetrics(win.SM_XVIRTUALSCREEN)),
		y:      int(win.GetSystemMetrics(win.SM_YVIRTUALSCREEN)),
		width:  int(win.GetSystemMetrics(win.SM_CXVIRTUALSCREEN)),
		height: int(win.GetSystemMetrics(win.SM_CYVIRTUALSCREEN)),
	}
}

// placementIntersectsVirtualScreen requires a meaningful overlap (not a
// single shared edge pixel) so the restored title bar remains reachable.
func placementIntersectsVirtualScreen(placement settings.WindowPlacement, screen screenRectangle) bool {
	const minimumVisible = 64
	if screen.width <= 0 || screen.height <= 0 {
		return false
	}
	overlapX := min(placement.X+placement.Width, screen.x+screen.width) - max(placement.X, screen.x)
	overlapY := min(placement.Y+placement.Height, screen.y+screen.height) - max(placement.Y, screen.y)
	return overlapX >= minimumVisible && overlapY >= minimumVisible
}
