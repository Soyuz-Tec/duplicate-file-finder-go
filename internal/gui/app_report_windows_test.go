//go:build windows

package gui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Soyuz-Tec/twintidy/internal/report"
	"github.com/Soyuz-Tec/twintidy/internal/scanner"
)

func TestDuplicateGroupsFromRowsRebuildsGroupsInDisplayOrder(t *testing.T) {
	rows := []duplicateRow{
		{Group: 3, Hash: "cc", Duplicate: true, File: scanner.FileRecord{Path: `C:\c1`, Size: 30}},
		{Group: 3, Hash: "cc", Duplicate: true, File: scanner.FileRecord{Path: `C:\c2`, Size: 30}},
		{Group: 1, Hash: "aa", Duplicate: true, File: scanner.FileRecord{Path: `C:\a1`, Size: 10}},
		{Group: 1, Hash: "aa", Duplicate: true, File: scanner.FileRecord{Path: `C:\a2`, Size: 10}},
	}
	groups := duplicateGroupsFromRows(rows)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if groups[0].Hash != "cc" || len(groups[0].Files) != 2 || groups[0].Size != 30 {
		t.Fatalf("first group mismatch: %#v", groups[0])
	}
	if groups[1].Hash != "aa" || groups[1].Files[1].Path != `C:\a2` {
		t.Fatalf("second group mismatch: %#v", groups[1])
	}
}

func TestDuplicateGroupsFromRowsSkipsNonDuplicates(t *testing.T) {
	rows := []duplicateRow{
		{Group: 1, Hash: "", Duplicate: false, File: scanner.FileRecord{Path: `C:\surface-only`, Size: 5}},
	}
	if groups := duplicateGroupsFromRows(rows); len(groups) != 0 {
		t.Fatalf("non-duplicate rows produced groups: %#v", groups)
	}
	if groups := duplicateGroupsFromRows(nil); len(groups) != 0 {
		t.Fatalf("nil rows produced groups: %#v", groups)
	}
}

func TestResolveReportDestinationUsesSelectedFilter(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		filterIndex int
		wantPath    string
		wantFormat  report.Format
	}{
		{"CSV default", `C:\out\report.csv`, 1, `C:\out\report.csv`, report.FormatCSV},
		{"JSON replaces CSV default", `C:\out\report.csv`, 2, `C:\out\report.json`, report.FormatJSON},
		{"CSV replaces JSON", `C:\out\report.JSON`, 1, `C:\out\report.csv`, report.FormatCSV},
		{"missing JSON extension", `C:\out\report`, 2, `C:\out\report.json`, report.FormatJSON},
		{"custom suffix retained", `C:\out\report.backup`, 1, `C:\out\report.backup.csv`, report.FormatCSV},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, format, err := resolveReportDestination(test.path, test.filterIndex)
			if err != nil {
				t.Fatal(err)
			}
			if path != test.wantPath || format != test.wantFormat {
				t.Fatalf("resolveReportDestination() = %q/%q, want %q/%q", path, format, test.wantPath, test.wantFormat)
			}
		})
	}
	if _, _, err := resolveReportDestination("  ", 1); err == nil {
		t.Fatal("empty destination was accepted")
	}
}

func TestNormalizedDestinationRequiresIndependentOverwriteCheck(t *testing.T) {
	if !pathsReferToSameDestination(`C:\Out\Report.csv`, `c:\out\report.CSV`) {
		t.Fatal("case-only Windows path difference was treated as another destination")
	}
	if pathsReferToSameDestination(`C:\out\report.csv`, `C:\out\report.json`) {
		t.Fatal("format-normalized destination was treated as the dialog destination")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if exists, err := resolvedReportDestinationExists(path); err != nil || exists {
		t.Fatalf("missing destination exists=%v err=%v", exists, err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if exists, err := resolvedReportDestinationExists(path); err != nil || !exists {
		t.Fatalf("existing destination exists=%v err=%v", exists, err)
	}
	if exists, err := resolvedReportDestinationExists(dir); err == nil || exists {
		t.Fatalf("directory destination exists=%v err=%v", exists, err)
	}
}

func TestWriteReportFileUsesAuthoritativeFormat(t *testing.T) {
	groups := []scanner.DuplicateGroup{
		{Size: 10, Hash: "aa", Files: []scanner.FileRecord{{Path: `C:\a1`, Size: 10}, {Path: `C:\a2`, Size: 10}}},
	}
	path := filepath.Join(t.TempDir(), "report.json")
	result := writeReportFile(context.Background(), path, report.FormatJSON, `C:\scanned`, groups, time.Now())
	if result.err != nil {
		t.Fatal(result.err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[0] != '{' {
		t.Fatalf("JSON export does not start with an object: %q", data)
	}
	if result.summary.GroupCount != 1 || result.summary.FileCount != 2 || result.bytes != int64(len(data)) {
		t.Fatalf("export result = %+v file bytes=%d", result, len(data))
	}
}

func TestDefaultReportFileName(t *testing.T) {
	name := defaultReportFileName(time.Date(2026, 7, 15, 9, 5, 4, 0, time.UTC))
	if name != "TwinTidy-duplicates-20260715-090504.csv" {
		t.Fatalf("defaultReportFileName = %q", name)
	}
}
