package scanner

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzShouldSkipDirectory asserts that directory protection never panics,
// is deterministic, and cannot be bypassed by placing a protected segment
// deeper in an arbitrary prefix.
func FuzzShouldSkipDirectory(f *testing.F) {
	f.Add("")
	f.Add(`C:\Users\person\Documents`)
	f.Add(`C:\Windows\System32`)
	f.Add(`relative\node_modules\package`)
	f.Add(`..\..\escape`)
	f.Add(`\\`)
	f.Add(`\\server\share\folder`)
	f.Add("mixed/separators\\folder")
	f.Fuzz(func(t *testing.T, path string) {
		first := ShouldSkipDirectory(path)
		second := ShouldSkipDirectory(path)
		if first != second {
			t.Fatalf("ShouldSkipDirectory(%q) was not deterministic: %v then %v", path, first, second)
		}

		// Joining can absorb the appended name into a UNC volume (a server or
		// share called Windows is not a protected directory) or fuse it onto a
		// drive-relative prefix ending in a colon, so the protection property
		// applies only when Windows survives as the standalone final segment
		// outside the volume name.
		protected := filepath.Join(path, "Windows")
		if filepath.Base(protected) == "Windows" &&
			!strings.Contains(strings.ToLower(filepath.VolumeName(protected)), "windows") {
			if !ShouldSkipDirectory(protected) {
				t.Fatalf("ShouldSkipDirectory(%q) did not protect an appended Windows segment", protected)
			}
		}
	})
}

// FuzzIsUserCreatedFilePath asserts that protected executable extensions are
// never classified as user-created files for any arbitrary prefix.
func FuzzIsUserCreatedFilePath(f *testing.F) {
	f.Add("")
	f.Add(`C:\Users\person\Documents\report`)
	f.Add(`weird names with spaces and unicode ⚡`)
	f.Add(`trailing.dot.`)
	f.Add(`..\relative`)
	f.Fuzz(func(t *testing.T, path string) {
		first := IsUserCreatedFilePath(path)
		second := IsUserCreatedFilePath(path)
		if first != second {
			t.Fatalf("IsUserCreatedFilePath(%q) was not deterministic: %v then %v", path, first, second)
		}

		executable := path + ".exe"
		if IsUserCreatedFilePath(executable) {
			t.Fatalf("IsUserCreatedFilePath(%q) accepted a protected executable extension", executable)
		}
	})
}

// FuzzCategoryForPath asserts that category classification never panics,
// always produces a defined category with a label, and ignores extension
// letter case.
func FuzzCategoryForPath(f *testing.F) {
	f.Add("document.PDF")
	f.Add("archive.tar.GZ")
	f.Add("no-extension")
	f.Add(".hidden")
	f.Add("движение.docx")
	f.Fuzz(func(t *testing.T, path string) {
		category := CategoryForPath(path)
		if !AllFileCategories()[category] {
			t.Fatalf("CategoryForPath(%q) returned undefined category %v", path, category)
		}
		if CategoryLabel(category) == "" {
			t.Fatalf("CategoryForPath(%q) returned category %v without a label", path, category)
		}
		if lowered := CategoryForPath(strings.ToLower(path)); lowered != category {
			t.Fatalf("CategoryForPath(%q) = %v but lowercase form classified as %v", path, category, lowered)
		}
	})
}
