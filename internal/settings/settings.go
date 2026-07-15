// Package settings persists non-authoritative user interface preferences.
// Settings are conveniences only: they must never carry scan results, file
// authority, or destructive intent, and a missing, corrupted, oversized, or
// future-schema file always degrades to defaults instead of failing startup.
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const (
	// Schema identifies the persisted layout. Readers treat any other value
	// as unknown and fall back to defaults rather than misinterpreting it.
	Schema = "twintidy.settings/v1"

	appFolderName = "TwinTidy"
	fileName      = "settings.json"

	// maxFileSize bounds how much of a settings file is read. A larger file
	// is not a plausible settings document and is ignored.
	maxFileSize = 1 << 20
)

// WindowPlacement records the last main-window bounds in native pixels.
type WindowPlacement struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Valid reports whether the placement describes a plausibly usable window.
// The GUI additionally verifies the rectangle against the current virtual
// screen before applying it.
func (p WindowPlacement) Valid() bool {
	const maxExtent = 32767
	return p.Width >= 200 && p.Height >= 200 &&
		p.Width <= maxExtent && p.Height <= maxExtent &&
		p.X >= -maxExtent && p.X <= maxExtent &&
		p.Y >= -maxExtent && p.Y <= maxExtent
}

// Settings is the persisted preference set. The focus filters are
// intentionally not persisted: each surface scan re-derives them from the
// scanned corpus, so a stored value would silently fight that flow.
type Settings struct {
	Schema     string           `json:"schema"`
	Window     *WindowPlacement `json:"window,omitempty"`
	LastFolder string           `json:"lastFolder,omitempty"`
}

// Defaults returns the settings used when nothing valid is persisted.
func Defaults() Settings {
	return Settings{Schema: Schema}
}

// DefaultPath resolves the per-user settings file location, preferring the
// same LocalAppData root the diagnostics package uses.
func DefaultPath() (string, error) {
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		return filepath.Join(localAppData, appFolderName, fileName), nil
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, appFolderName, fileName), nil
}

// Load reads settings from path. Every failure mode returns defaults: user
// preferences are not worth blocking startup, and a corrupted file must not
// be trusted.
func Load(path string) Settings {
	defaults := Defaults()
	if path == "" {
		return defaults
	}

	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileSize {
		return defaults
	}

	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxFileSize {
		return defaults
	}

	var loaded Settings
	if err := json.Unmarshal(data, &loaded); err != nil {
		return defaults
	}
	if loaded.Schema != Schema {
		return defaults
	}
	if loaded.Window != nil && !loaded.Window.Valid() {
		loaded.Window = nil
	}
	return loaded
}

// Save writes settings atomically: the document is staged beside the target
// and renamed into place so a crash cannot leave a torn file.
func Save(path string, value Settings) error {
	if path == "" {
		return errors.New("settings path is empty")
	}
	value.Schema = Schema

	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	staging, err := os.CreateTemp(dir, fileName+".staging-*")
	if err != nil {
		return err
	}
	stagingPath := staging.Name()
	if _, err := staging.Write(data); err != nil {
		_ = staging.Close()
		_ = os.Remove(stagingPath)
		return err
	}
	if err := staging.Close(); err != nil {
		_ = os.Remove(stagingPath)
		return err
	}
	if err := os.Rename(stagingPath, path); err != nil {
		_ = os.Remove(stagingPath)
		return err
	}
	return nil
}
