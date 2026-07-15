package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func settingsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nested", "settings.json")
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	loaded := Load(filepath.Join(t.TempDir(), "absent.json"))
	if loaded != Defaults() {
		t.Fatalf("missing file did not load defaults: %#v", loaded)
	}
}

func TestLoadEmptyPathReturnsDefaults(t *testing.T) {
	if loaded := Load(""); loaded != Defaults() {
		t.Fatalf("empty path did not load defaults: %#v", loaded)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := settingsPath(t)
	saved := Settings{
		Window:     &WindowPlacement{X: 40, Y: 60, Width: 1440, Height: 860},
		LastFolder: `C:\Users\person\Documents`,
	}
	if err := Save(path, saved); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded := Load(path)
	if loaded.Schema != Schema {
		t.Fatalf("loaded schema %q", loaded.Schema)
	}
	if loaded.Window == nil || *loaded.Window != *saved.Window {
		t.Fatalf("window did not round-trip: %#v", loaded.Window)
	}
	if loaded.LastFolder != saved.LastFolder {
		t.Fatalf("last folder did not round-trip: %q", loaded.LastFolder)
	}
}

func TestLoadCorruptedFileReturnsDefaults(t *testing.T) {
	path := settingsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if loaded := Load(path); loaded != Defaults() {
		t.Fatalf("corrupted file did not load defaults: %#v", loaded)
	}
}

func TestLoadUnknownSchemaReturnsDefaults(t *testing.T) {
	path := settingsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	document := `{"schema":"twintidy.settings/v999","lastFolder":"C:\\somewhere"}`
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if loaded := Load(path); loaded != Defaults() {
		t.Fatalf("future schema did not load defaults: %#v", loaded)
	}
}

func TestLoadOversizedFileReturnsDefaults(t *testing.T) {
	path := settingsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	huge := strings.Repeat(" ", maxFileSize+1)
	if err := os.WriteFile(path, []byte(huge), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if loaded := Load(path); loaded != Defaults() {
		t.Fatalf("oversized file did not load defaults: %#v", loaded)
	}
}

func TestLoadDropsImplausibleWindowPlacement(t *testing.T) {
	path := settingsPath(t)
	if err := Save(path, Settings{Window: &WindowPlacement{X: 0, Y: 0, Width: 10, Height: 10}}); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if loaded := Load(path); loaded.Window != nil {
		t.Fatalf("implausible placement survived load: %#v", loaded.Window)
	}
}

func TestSaveCreatesParentAndLeavesNoStaging(t *testing.T) {
	path := settingsPath(t)
	if err := Save(path, Defaults()); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "settings.json" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("unexpected directory contents after save: %v", names)
	}
}

func TestSaveOverwritesExisting(t *testing.T) {
	path := settingsPath(t)
	if err := Save(path, Settings{LastFolder: "first"}); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}
	if err := Save(path, Settings{LastFolder: "second"}); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}
	if loaded := Load(path); loaded.LastFolder != "second" {
		t.Fatalf("overwrite did not persist: %q", loaded.LastFolder)
	}
}

func TestWindowPlacementValidation(t *testing.T) {
	cases := []struct {
		name      string
		placement WindowPlacement
		valid     bool
	}{
		{"typical", WindowPlacement{X: 100, Y: 100, Width: 1440, Height: 860}, true},
		{"negative origin on secondary monitor", WindowPlacement{X: -1920, Y: 0, Width: 1280, Height: 720}, true},
		{"too small", WindowPlacement{X: 0, Y: 0, Width: 199, Height: 400}, false},
		{"absurd width", WindowPlacement{X: 0, Y: 0, Width: 40000, Height: 400}, false},
		{"absurd origin", WindowPlacement{X: -40000, Y: 0, Width: 800, Height: 600}, false},
	}
	for _, testCase := range cases {
		if testCase.placement.Valid() != testCase.valid {
			t.Fatalf("%s: Valid() = %v, want %v", testCase.name, testCase.placement.Valid(), testCase.valid)
		}
	}
}
