package main

import (
	"fmt"
	"os"

	"duplicate-file-finder-go/internal/diagnostics"
	"duplicate-file-finder-go/internal/gui"
)

func main() {
	if err := diagnostics.Init("DuplicateFileFinder"); err != nil {
		fmt.Fprintln(os.Stderr, "diagnostics initialization failed:", err)
	} else {
		defer diagnostics.Close()
	}

	defer func() {
		if err := diagnostics.RecoverToError("main", nil); err != nil {
			diagnostics.Logf("%v", err)
		}
	}()

	diagnostics.Logf("app starting")
	gui.Run()
	diagnostics.Logf("app exited")
}
