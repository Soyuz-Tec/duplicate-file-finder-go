//go:build windows

package gui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Soyuz-Tec/duplicate-file-finder-go/internal/scanner"
)

func TestSnapshotPreviewRowsIsImmutableFromSourceMutation(t *testing.T) {
	rows := []duplicateRow{{
		Key:  "first",
		Hash: "hash-one",
		File: scanner.FileRecord{Path: `C:\Data\first.pdf`, Size: 10},
	}}

	snapshot := snapshotPreviewRows(rows)
	rows[0].Key = "changed"
	rows[0].Hash = "hash-two"
	rows[0].File.Path = `D:\Changed\second.pdf`
	rows[0].File.Size = 20

	if snapshot[0].Key != "first" || snapshot[0].Hash != "hash-one" || snapshot[0].File.Path != `C:\Data\first.pdf` || snapshot[0].File.Size != 10 {
		t.Fatalf("preview snapshot changed with source: %+v", snapshot[0])
	}
}

func TestApplyPreparedPreviewRejectsStaleTokenBeforeWidgetAccess(t *testing.T) {
	previewState := previewGenerationState{}
	stale := previewState.begin(9)
	latest := previewState.begin(9)
	app := &windowsApp{
		operation:    operationState{phase: phaseResultsReady, folderRevision: 9},
		previewState: previewState,
	}

	app.applyPreparedPreview(preparedPreview{token: stale, mode: previewModeSingle, details: "stale"})
	if !app.previewState.accepts(latest, 9) {
		t.Fatal("stale result mutated the latest preview generation")
	}
}

func TestInvalidatePreviewCancelsWorkAndRejectsToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	app := &windowsApp{operation: operationState{folderRevision: 3}}
	token := app.previewState.begin(3)
	app.previewCancel = cancel

	app.invalidatePreview()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("preview context error = %v, want cancellation", ctx.Err())
	}
	if app.previewState.accepts(token, 3) {
		t.Fatal("invalidated preview token remains acceptable")
	}
}

func TestCleanupPreviewArtifactRemovesOnlyOwnedTempDirectory(t *testing.T) {
	dir, err := os.MkdirTemp(os.TempDir(), "twintidy-preview-")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "preview.txt")
	if err := os.WriteFile(file, []byte("preview"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanupPreviewArtifact(dir)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned preview directory still exists or returned unexpected error: %v", err)
	}
}

func TestResolvedPreviewArtifactMatchesParentJunctionButRejectsTargetRedirect(t *testing.T) {
	tempRoot := `D:\a\_temp`
	target := `D:\a\_temp\twintidy-preview-123`
	resolvedTempRoot := `D:\hostedtoolcache\runner-temp`
	resolvedTarget := `D:\hostedtoolcache\runner-temp\twintidy-preview-123`
	if !resolvedPreviewArtifactMatches(tempRoot, target, resolvedTempRoot, resolvedTarget) {
		t.Fatal("parent-junction resolution was rejected")
	}
	if resolvedPreviewArtifactMatches(tempRoot, target, resolvedTempRoot, `D:\victim\data`) {
		t.Fatal("redirected artifact target was accepted")
	}
}

func TestCancelledComparisonPreviewCreatesNoArtifact(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	page, rendered, dir, err := buildComparisonPreviewPage(ctx, []duplicateRow{{}}, 1, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if page != "" || rendered != 0 || dir != "" {
		t.Fatalf("cancelled preview returned artifact page=%q rendered=%d dir=%q", page, rendered, dir)
	}
}

func TestComparisonArtifactOmitsAbsoluteSourcePaths(t *testing.T) {
	sourcePath := `C:\Users\private\sensitive-folder\report.txt`
	rows := []duplicateRow{{
		Status: "Scanned",
		File:   scanner.FileRecord{Path: sourcePath, Size: 42},
	}}

	page, rendered, dir, err := buildComparisonPreviewPage(context.Background(), rows, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupPreviewArtifact(dir) })
	if rendered != 1 {
		t.Fatalf("rendered = %d, want 1", rendered)
	}
	contents, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), sourcePath) {
		t.Fatal("comparison artifact contains the absolute source path")
	}
	if !strings.Contains(string(contents), "report.txt") {
		t.Fatal("comparison artifact omitted the human-readable file name")
	}
}

func TestComparisonPDFFallbackDoesNotReadOrPersistDocumentText(t *testing.T) {
	fallback := fallbackForComparisonRow(duplicateRow{
		File: scanner.FileRecord{Path: `C:\Missing\sensitive.pdf`},
	})
	if fallback.Badge != "PDF" || !strings.Contains(fallback.Text, "not copied") {
		t.Fatalf("privacy-preserving PDF fallback = %+v", fallback)
	}
}

func TestRiskyLocalFilesNeverReceiveAnEmbeddedBrowserRoute(t *testing.T) {
	for _, path := range []string{
		`C:\Missing\document.pdf`,
		`C:\Missing\macro.docm`,
		`C:\Missing\macro.xlsm`,
		`C:\Missing\macro.pptm`,
		`C:\Missing\media.mp4`,
	} {
		t.Run(filepath.Ext(path), func(t *testing.T) {
			request := previewRequest{
				ctx:  context.Background(),
				mode: previewModeSingle,
				rows: []duplicateRow{{File: scanner.FileRecord{Path: path}}},
			}
			result := preparePreview(request, false)
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.pagePath != "" || result.tempDir != "" {
				t.Fatalf("risky file received an embedded page route: %+v", result)
			}
			if !strings.Contains(result.details, "Show In Explorer") {
				t.Fatalf("safe external-inspection guidance missing: %q", result.details)
			}
		})
	}
}

func TestSinglePDFFallbackDoesNotParseDocumentContents(t *testing.T) {
	result := preparePreview(previewRequest{
		ctx:  context.Background(),
		mode: previewModeSingle,
		rows: []duplicateRow{{File: scanner.FileRecord{Path: `C:\Missing\secret.pdf`}}},
	}, false)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !strings.Contains(result.details, "does not parse PDF contents in-process") {
		t.Fatalf("PDF fallback did not state the parser-free policy: %q", result.details)
	}
}

func TestTextPreviewRequiresScopedStableRecordAndReadsLegitimateText(t *testing.T) {
	dir, err := os.MkdirTemp(".", "twintidy-preview-scope-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("verified preview text"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readVerifiedTextPreview(scanner.FileRecord{Path: path}); err == nil {
		t.Fatal("unscoped path reached the text preview reader")
	}
	if _, err := shellThumbnailForVerifiedRecord(scanner.FileRecord{Path: path}, 64); err == nil {
		t.Fatal("unscoped path reached the Windows Shell thumbnail provider")
	}

	report, err := scanner.NewEngine(1).SurfaceScan(context.Background(), []string{dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Files) != 1 {
		t.Fatalf("surface records = %d, want 1", len(report.Files))
	}
	text, err := readVerifiedTextPreview(report.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "verified preview text") {
		t.Fatalf("legitimate bounded text preview = %q", text)
	}

	if err := os.WriteFile(path, []byte("replacement content with a different size"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readVerifiedTextPreview(report.Files[0]); err == nil {
		t.Fatal("changed file was accepted by the text preview reader")
	}
}

func TestScanCrashFieldsExcludeFolderAndFilePaths(t *testing.T) {
	token := operationToken{
		generation:     14,
		folderRevision: 6,
		folder:         `C:\Users\private\sensitive-folder`,
	}
	fields := scanCrashFields(token)

	if len(fields) != 2 {
		t.Fatalf("crash fields = %v, want generation metadata only", fields)
	}
	for key, value := range fields {
		if strings.Contains(strings.ToLower(key), "path") || strings.Contains(value, token.folder) {
			t.Fatalf("crash field exposed a path: %q=%q", key, value)
		}
	}
}
