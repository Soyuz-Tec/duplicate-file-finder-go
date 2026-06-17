package scanner

import (
	"os"
	"path/filepath"
	"strings"
)

var protectedDirectoryNames = map[string]struct{}{
	"$recycle.bin":              {},
	"system volume information": {},
	"windows":                   {},
	"program files":             {},
	"program files (x86)":       {},
	"programdata":               {},
	"recovery":                  {},
	"perflogs":                  {},
	"config.msi":                {},
	"msocache":                  {},
	"appdata":                   {},
	"application data":          {},
	"temporary internet files":  {},
	"inetcache":                 {},
	"cache":                     {},
	".cache":                    {},
	".git":                      {},
	".hg":                       {},
	".svn":                      {},
	"node_modules":              {},
	"__pycache__":               {},
	".pytest_cache":             {},
	".mypy_cache":               {},
	".ruff_cache":               {},
	".venv":                     {},
	"venv":                      {},
	"env":                       {},
	"site-packages":             {},
	"packages":                  {},
	"bin":                       {},
	"obj":                       {},
	"target":                    {},
	"build":                     {},
	"dist":                      {},
}

var protectedFileExtensions = map[string]struct{}{
	".386":        {},
	".appx":       {},
	".appxbundle": {},
	".cab":        {},
	".com":        {},
	".cpl":        {},
	".cur":        {},
	".dll":        {},
	".drv":        {},
	".efi":        {},
	".exe":        {},
	".gadget":     {},
	".hta":        {},
	".icl":        {},
	".icns":       {},
	".ico":        {},
	".inf":        {},
	".ins":        {},
	".iso":        {},
	".job":        {},
	".lnk":        {},
	".msi":        {},
	".msix":       {},
	".msixbundle": {},
	".msp":        {},
	".mst":        {},
	".ocx":        {},
	".pif":        {},
	".scr":        {},
	".sys":        {},
	".theme":      {},
	".themepack":  {},
}

func IsUserCreatedFilePath(path string) bool {
	if path == "" {
		return false
	}
	if ShouldSkipDirectory(filepath.Dir(path)) {
		return false
	}
	_, protected := protectedFileExtensions[strings.ToLower(filepath.Ext(path))]
	return !protected
}

func ShouldSkipDirectory(path string) bool {
	if path == "" {
		return false
	}

	clean := filepath.Clean(path)
	if isInsideKnownSystemRoot(clean) {
		return true
	}

	for _, segment := range pathSegments(clean) {
		if _, protected := protectedDirectoryNames[strings.ToLower(segment)]; protected {
			return true
		}
	}
	return false
}

func isInsideKnownSystemRoot(path string) bool {
	for _, envName := range []string{"WINDIR", "SystemRoot", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
		root := os.Getenv(envName)
		if root == "" {
			continue
		}
		if isSameOrInside(path, root) {
			return true
		}
	}
	return false
}

func isSameOrInside(path string, root string) bool {
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}

	rel, err := filepath.Rel(filepath.Clean(rootAbs), filepath.Clean(pathAbs))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func pathSegments(path string) []string {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	if volume != "" {
		clean = strings.TrimPrefix(clean, volume)
	}
	clean = strings.Trim(clean, string(filepath.Separator))
	if clean == "" {
		return nil
	}
	return strings.Split(clean, string(filepath.Separator))
}
