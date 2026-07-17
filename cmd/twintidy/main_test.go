package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Soyuz-Tec/twintidy/internal/buildinfo"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run --version code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), buildinfo.Version) {
		t.Fatalf("version output %q does not contain %q", stdout.String(), buildinfo.Version)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestRunHelp(t *testing.T) {
	for _, argument := range []string{"--help", "-h"} {
		t.Run(argument, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{argument}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("run %s code = %d, want 0", argument, code)
			}
			for _, expected := range []string{"TwinTidy", "--version", "--ui-smoke-test"} {
				if !strings.Contains(stdout.String(), expected) {
					t.Fatalf("help output %q does not contain %q", stdout.String(), expected)
				}
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %q", stderr.String())
			}
		})
	}
}

func TestRunRejectsUnknownOption(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--unknown"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run unknown option code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown option") {
		t.Fatalf("stderr = %q, want unknown option message", stderr.String())
	}
}

func TestRunUISmokeTestPropagatesFailure(t *testing.T) {
	original := runSmokeTest
	t.Cleanup(func() { runSmokeTest = original })
	runSmokeTest = func() error { return errors.New("smoke failed") }

	var stdout, stderr bytes.Buffer
	code := run([]string{"--ui-smoke-test"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run smoke failure code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "smoke failed") {
		t.Fatalf("stderr = %q, want smoke failure", stderr.String())
	}
}

func TestRunPropagatesGUIFailure(t *testing.T) {
	originalInit, originalClose, originalGUI, originalReport := initDiagnostics, closeDiagnostics, runGUI, reportFatal
	t.Cleanup(func() {
		initDiagnostics, closeDiagnostics, runGUI, reportFatal = originalInit, originalClose, originalGUI, originalReport
	})

	initDiagnostics = func(string) error { return nil }
	closeDiagnostics = func() {}
	runGUI = func() error { return errors.New("window failed") }
	reported := ""
	reportFatal = func(_ string, message string) { reported = message }

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run GUI failure code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "window failed") || !strings.Contains(reported, "window failed") {
		t.Fatalf("failure not reported: stderr=%q report=%q", stderr.String(), reported)
	}
}

func TestRunRecoversGUIPanicAndClosesDiagnostics(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	originalInit, originalClose, originalGUI, originalReport := initDiagnostics, closeDiagnostics, runGUI, reportFatal
	t.Cleanup(func() {
		initDiagnostics, closeDiagnostics, runGUI, reportFatal = originalInit, originalClose, originalGUI, originalReport
	})

	closed := 0
	reported := ""
	initDiagnostics = func(string) error { return nil }
	closeDiagnostics = func() { closed++ }
	runGUI = func() error { panic("unexpected GUI panic") }
	reportFatal = func(_ string, message string) { reported = message }

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run panic code = %d, want 1", code)
	}
	if closed != 1 {
		t.Fatalf("diagnostics closed %d times, want 1", closed)
	}
	if !strings.Contains(stderr.String(), "unexpected internal error") || !strings.Contains(reported, "unexpected internal error") {
		t.Fatalf("panic not surfaced: stderr=%q report=%q", stderr.String(), reported)
	}
}

func TestRunRecoversSmokePanicWithoutInteractiveDialog(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	originalSmoke, originalReport := runSmokeTest, reportFatal
	t.Cleanup(func() {
		runSmokeTest, reportFatal = originalSmoke, originalReport
	})

	runSmokeTest = func() error { panic("unexpected smoke panic") }
	reportCalls := 0
	reportFatal = func(string, string) { reportCalls++ }

	var stdout, stderr bytes.Buffer
	code := run([]string{"--ui-smoke-test"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run smoke panic code = %d, want 1", code)
	}
	if reportCalls != 0 {
		t.Fatalf("non-interactive smoke panic opened %d fatal dialog(s)", reportCalls)
	}
	if !strings.Contains(stderr.String(), "unexpected internal error") {
		t.Fatalf("smoke panic was not written to stderr: %q", stderr.String())
	}
}
