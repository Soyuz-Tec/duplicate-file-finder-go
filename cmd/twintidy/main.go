package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Soyuz-Tec/twintidy/internal/buildinfo"
	"github.com/Soyuz-Tec/twintidy/internal/diagnostics"
	"github.com/Soyuz-Tec/twintidy/internal/gui"
	"github.com/Soyuz-Tec/twintidy/internal/startup"
)

var (
	initDiagnostics  = diagnostics.Init
	closeDiagnostics = diagnostics.Close
	runGUI           = gui.Run
	runSmokeTest     = gui.SmokeTest
	reportFatal      = startup.ReportFatal
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) (exitCode int) {
	diagnosticsInitialized := false
	defer func() {
		recovered := recover()
		if err := diagnostics.PanicToError("main", recovered, nil); err != nil {
			diagnostics.Logf("%v", err)
			fmt.Fprintln(stderr, err)
			if len(args) == 0 {
				reportFatal("TwinTidy stopped unexpectedly", err.Error())
			}
			exitCode = 1
		}
		if diagnosticsInitialized {
			closeDiagnostics()
		}
	}()

	if len(args) == 1 {
		switch args[0] {
		case "--help", "-h":
			printUsage(stdout)
			return 0
		case "--version":
			fmt.Fprintln(stdout, buildinfo.Summary())
			return 0
		case "--ui-smoke-test":
			if err := runSmokeTest(); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		}
	}
	if len(args) > 0 {
		message := fmt.Sprintf("unknown option: %s", args[0])
		fmt.Fprintln(stderr, message)
		return 2
	}

	if err := initDiagnostics("TwinTidy"); err != nil {
		message := fmt.Sprintf("diagnostics initialization failed: %v", err)
		fmt.Fprintln(stderr, message)
		reportFatal("TwinTidy could not start", message)
		return 1
	}
	diagnosticsInitialized = true

	diagnostics.Logf("app starting")
	if err := runGUI(); err != nil {
		diagnostics.Logf("app startup failed: %v", err)
		fmt.Fprintln(stderr, err)
		reportFatal("TwinTidy could not start", err.Error())
		return 1
	}
	diagnostics.Logf("app exited")
	return 0
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "TwinTidy - find and review exact duplicate files")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  TwinTidy.exe                 Launch the desktop application")
	fmt.Fprintln(output, "  TwinTidy.exe --version       Print build identity")
	fmt.Fprintln(output, "  TwinTidy.exe --ui-smoke-test Create and dispose the main window")
	fmt.Fprintln(output, "  TwinTidy.exe --help          Show this help")
}
