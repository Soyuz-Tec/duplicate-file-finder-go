//go:build windows

package gui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// trustedExplorerPath resolves Explorer from the Windows installation
// directory. Passing the absolute result to os/exec avoids PATH and current-
// directory executable search.
func trustedExplorerPath() (string, error) {
	windowsDir, err := windows.GetWindowsDirectory()
	if err != nil {
		return "", fmt.Errorf("resolve Windows directory: %w", err)
	}
	if windowsDir == "" || !filepath.IsAbs(windowsDir) {
		return "", errors.New("the Windows installation directory is invalid")
	}

	explorer := filepath.Clean(filepath.Join(windowsDir, "explorer.exe"))
	if !strings.EqualFold(filepath.Dir(explorer), filepath.Clean(windowsDir)) {
		return "", errors.New("resolved Explorer path escaped the Windows directory")
	}
	info, err := os.Stat(explorer)
	if err != nil {
		return "", fmt.Errorf("verify Windows Explorer: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("resolved Windows Explorer is not a regular file")
	}
	return explorer, nil
}
