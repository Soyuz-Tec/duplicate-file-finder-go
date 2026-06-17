package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestScanFindsExactMatchesWithDifferentNames(t *testing.T) {
	root := userFileTestRoot(t)
	content := []byte("invoice duplicate payload")
	writeTestFile(t, filepath.Join(root, "January invoice.pdf"), content)
	writeTestFile(t, filepath.Join(root, "renamed-copy.bin"), content)
	writeTestFile(t, filepath.Join(root, "same-size-not-duplicate.txt"), []byte("different file payload!"))

	engine := NewEngine(2)
	groups, err := engine.Scan(context.Background(), []string{root}, make(chan Progress, 128))
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 duplicate group, got %d: %#v", len(groups), groups)
	}
	if len(groups[0].Files) != 2 {
		t.Fatalf("expected 2 duplicate files, got %d", len(groups[0].Files))
	}

	paths := map[string]bool{}
	for _, file := range groups[0].Files {
		paths[filepath.Base(file.Path)] = true
	}
	if !paths["January invoice.pdf"] || !paths["renamed-copy.bin"] {
		t.Fatalf("duplicate group did not include expected differently named files: %#v", paths)
	}
}

func TestScanRejectsSameSizeDifferentContent(t *testing.T) {
	root := userFileTestRoot(t)
	writeTestFile(t, filepath.Join(root, "a.txt"), []byte("same-size-a"))
	writeTestFile(t, filepath.Join(root, "b.txt"), []byte("same-size-b"))

	engine := NewEngine(2)
	groups, err := engine.Scan(context.Background(), []string{root}, make(chan Progress, 128))
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("expected no duplicates, got %#v", groups)
	}
}

func TestScanRejectsSameBoundaryDifferentMiddle(t *testing.T) {
	root := userFileTestRoot(t)
	head := repeatByte('h', boundaryReadSize)
	tail := repeatByte('t', boundaryReadSize)
	left := append(append([]byte{}, head...), repeatByte('a', boundaryReadSize)...)
	left = append(left, tail...)
	right := append(append([]byte{}, head...), repeatByte('b', boundaryReadSize)...)
	right = append(right, tail...)

	writeTestFile(t, filepath.Join(root, "left.bin"), left)
	writeTestFile(t, filepath.Join(root, "right.bin"), right)

	engine := NewEngine(2)
	groups, err := engine.Scan(context.Background(), []string{root}, make(chan Progress, 128))
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("expected full hashing to reject boundary-only matches, got %#v", groups)
	}
}

func TestScanSkipsSystemAndApplicationFiles(t *testing.T) {
	root := userFileTestRoot(t)
	content := []byte("same duplicate bytes")
	writeTestFile(t, filepath.Join(root, "keep-a.pdf"), content)
	writeTestFile(t, filepath.Join(root, "keep-b.pdf"), content)
	writeTestFile(t, filepath.Join(root, "setup-a.exe"), content)
	writeTestFile(t, filepath.Join(root, "setup-b.exe"), content)
	writeTestFile(t, filepath.Join(root, "node_modules", "package-a.pdf"), []byte("dependency duplicate"))
	writeTestFile(t, filepath.Join(root, "node_modules", "package-b.pdf"), []byte("dependency duplicate"))

	engine := NewEngine(2)
	groups, err := engine.Scan(context.Background(), []string{root}, make(chan Progress, 128))
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected only user PDF duplicate group, got %d: %#v", len(groups), groups)
	}
	for _, file := range groups[0].Files {
		name := filepath.Base(file.Path)
		if name != "keep-a.pdf" && name != "keep-b.pdf" {
			t.Fatalf("scanner included non-user file %q", file.Path)
		}
	}
}

func TestSurfaceScanCategorizesUserFiles(t *testing.T) {
	root := userFileTestRoot(t)
	writeTestFile(t, filepath.Join(root, "a.pdf"), []byte("pdf"))
	writeTestFile(t, filepath.Join(root, "b.docx"), []byte("word"))
	writeTestFile(t, filepath.Join(root, "c.xlsx"), []byte("excel"))
	writeTestFile(t, filepath.Join(root, "d.txt"), []byte("text"))
	writeTestFile(t, filepath.Join(root, "ignored.exe"), []byte("app"))

	engine := NewEngine(2)
	report, err := engine.SurfaceScan(context.Background(), []string{root}, make(chan Progress, 128))
	if err != nil {
		t.Fatalf("SurfaceScan returned error: %v", err)
	}
	if report.TotalFiles != 4 {
		t.Fatalf("expected 4 user files, got %d", report.TotalFiles)
	}
	if report.CategoryStats[CategoryPDF].Files != 1 ||
		report.CategoryStats[CategoryWord].Files != 1 ||
		report.CategoryStats[CategoryExcel].Files != 1 ||
		report.CategoryStats[CategoryText].Files != 1 {
		t.Fatalf("unexpected category stats: %#v", report.CategoryStats)
	}
	if report.SkippedSystemItems == 0 {
		t.Fatalf("expected executable to be skipped")
	}
}

func TestScanFilesHonorsCategoryFilter(t *testing.T) {
	root := userFileTestRoot(t)
	pdfPayload := []byte("pdf duplicate payload")
	wordPayload := []byte("word duplicate payload")
	writeTestFile(t, filepath.Join(root, "a.pdf"), pdfPayload)
	writeTestFile(t, filepath.Join(root, "b.pdf"), pdfPayload)
	writeTestFile(t, filepath.Join(root, "a.docx"), wordPayload)
	writeTestFile(t, filepath.Join(root, "b.docx"), wordPayload)

	engine := NewEngine(2)
	report, err := engine.SurfaceScan(context.Background(), []string{root}, make(chan Progress, 128))
	if err != nil {
		t.Fatalf("SurfaceScan returned error: %v", err)
	}

	groups, err := engine.ScanFiles(context.Background(), report.Files, ScanOptions{
		Categories:    map[FileCategory]bool{CategoryPDF: true},
		UserFilesOnly: true,
	}, make(chan Progress, 128))
	if err != nil {
		t.Fatalf("ScanFiles returned error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected one PDF group, got %d: %#v", len(groups), groups)
	}
	for _, file := range groups[0].Files {
		if filepath.Ext(file.Path) != ".pdf" {
			t.Fatalf("expected only PDF records, got %s", file.Path)
		}
	}
}

func TestDeleteFilesFallsBackToPermanentDelete(t *testing.T) {
	root := userFileTestRoot(t)
	path := filepath.Join(root, "remove-me.txt")
	writeTestFile(t, path, []byte("delete"))

	originalTrashThrow := trashThrow
	trashThrow = func(...string) error {
		return errors.New("trash unavailable")
	}
	defer func() {
		trashThrow = originalTrashThrow
	}()

	result := DeleteFiles([]string{path}, true)
	if len(result.Failed) != 0 {
		t.Fatalf("expected no failures, got %#v", result.Failed)
	}
	if len(result.Deleted) != 1 {
		t.Fatalf("expected one deleted file, got %#v", result.Deleted)
	}
	if result.Deleted[0].Action != DeleteActionPermanent {
		t.Fatalf("expected permanent fallback, got %s", result.Deleted[0].Action)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected file to be removed, stat err=%v", err)
	}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
}

func userFileTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(".", "scanner-test-")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("Abs failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(absRoot)
	})
	return absRoot
}

func repeatByte(b byte, count int) []byte {
	data := make([]byte, count)
	for i := range data {
		data[i] = b
	}
	return data
}
