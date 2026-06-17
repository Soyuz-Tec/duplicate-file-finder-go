//go:build !windows

package gui

import (
	"fmt"
	"os"
)

func Run() {
	fmt.Fprintln(os.Stderr, "This build currently provides a Windows-native desktop GUI. Run it on Windows or add a platform GUI for this OS.")
}
