package diagnostics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCrashReportIsWrittenToLocalAppData(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("LOCALAPPDATA", tempDir)

	resetForTest(t)
	if err := Init("DuplicateFileFinderTest"); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer func() {
		Close()
		resetForTest(t)
	}()

	Logf("diagnostics test message")
	path := WriteCrashReport("unit test", "boom", []byte("stack line\n"), map[string]string{"operation": "test"})
	if path == "" {
		t.Fatal("expected crash report path")
	}

	expectedRoot := filepath.Join(tempDir, "DuplicateFileFinderTest", "logs")
	if !strings.HasPrefix(path, expectedRoot) {
		t.Fatalf("crash report path %q does not start with %q", path, expectedRoot)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read crash report: %v", err)
	}
	report := string(data)
	for _, want := range []string{
		"Duplicate File Finder Crash Report",
		"Scope: unit test",
		"Panic: boom",
		"- operation: test",
		"stack line",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("crash report missing %q:\n%s", want, report)
		}
	}

	if SessionLogPath() == "" {
		t.Fatal("expected session log path")
	}
}

func resetForTest(t *testing.T) {
	t.Helper()

	mu.Lock()
	defer mu.Unlock()

	if logFile != nil {
		_ = logFile.Close()
	}
	logFile = nil
	logDirPath = ""
	sessionLogPath = ""
}
