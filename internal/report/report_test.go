package report

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Soyuz-Tec/twintidy/internal/scanner"
)

func sampleGroups() []scanner.DuplicateGroup {
	modified := time.Date(2026, 7, 1, 10, 30, 0, 0, time.UTC)
	created := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	return []scanner.DuplicateGroup{
		{
			Size: 2048,
			Hash: "aa11",
			Files: []scanner.FileRecord{
				{Path: `C:\docs\report.pdf`, Size: 2048, CreatedAt: created, ModifiedAt: modified, Category: scanner.CategoryPDF},
				{Path: `C:\backup\report copy.pdf`, Size: 2048, CreatedAt: created, ModifiedAt: modified, Category: scanner.CategoryPDF},
				{Path: `C:\backup\report copy 2.pdf`, Size: 2048, CreatedAt: created, ModifiedAt: modified, Category: scanner.CategoryPDF},
			},
		},
		{
			Size: 100,
			Hash: "bb22",
			Files: []scanner.FileRecord{
				{Path: `=SUM(A1:A9).txt`, Size: 100, ModifiedAt: modified, Category: scanner.CategoryText},
				{Path: `C:\notes\copy.txt`, Size: 100, ModifiedAt: modified, Category: scanner.CategoryText},
			},
		},
	}
}

func TestBuildDocumentCountsAndEstimate(t *testing.T) {
	document := BuildDocument(`C:\scanned`, sampleGroups(), time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC))
	if document.Schema != Schema {
		t.Fatalf("schema = %q", document.Schema)
	}
	if document.GeneratedAt != "2026-07-15T12:00:00Z" {
		t.Fatalf("generatedAt = %q", document.GeneratedAt)
	}
	if document.GroupCount != 2 || document.FileCount != 5 {
		t.Fatalf("counts = %d groups, %d files", document.GroupCount, document.FileCount)
	}
	// Keeping one copy per group: 2 extra PDFs at 2048 plus 1 extra text at 100.
	if document.ReclaimableBytes != 2*2048+100 {
		t.Fatalf("reclaimableBytes = %d", document.ReclaimableBytes)
	}
}

func TestBuildDocumentEmpty(t *testing.T) {
	document := BuildDocument("", nil, time.Unix(0, 0))
	if document.GroupCount != 0 || document.FileCount != 0 || document.ReclaimableBytes != 0 {
		t.Fatalf("empty document has non-zero counts: %#v", document)
	}
	if len(document.Groups) != 0 {
		t.Fatalf("empty document has groups: %#v", document.Groups)
	}
}

func TestJSONDocumentRoundTrips(t *testing.T) {
	document := BuildDocument(`C:\scanned`, sampleGroups(), time.Now())
	data, err := document.MarshalJSONDocument()
	if err != nil {
		t.Fatalf("MarshalJSONDocument failed: %v", err)
	}
	if data[len(data)-1] != '\n' {
		t.Fatal("JSON document does not end with a newline")
	}

	var decoded Document
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if decoded.Schema != Schema || decoded.GroupCount != 2 || len(decoded.Groups) != 2 {
		t.Fatalf("decoded document mismatch: %#v", decoded)
	}
	if decoded.Groups[0].Files[0].Path != `C:\docs\report.pdf` {
		t.Fatalf("decoded path mismatch: %q", decoded.Groups[0].Files[0].Path)
	}
}

func TestWriteStreamsJSON(t *testing.T) {
	generatedAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	var buffer bytes.Buffer
	summary, err := Write(
		context.Background(),
		&buffer,
		FormatJSON,
		`C:\scanned`,
		sampleGroups(),
		generatedAt,
	)
	if err != nil {
		t.Fatalf("Write JSON failed: %v", err)
	}
	if summary.GroupCount != 2 || summary.FileCount != 5 || summary.ReclaimableBytes != 4196 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.BytesWritten != int64(buffer.Len()) {
		t.Fatalf("BytesWritten = %d, buffer length = %d", summary.BytesWritten, buffer.Len())
	}
	if !bytes.HasSuffix(buffer.Bytes(), []byte("\n")) {
		t.Fatal("streamed JSON does not end with a newline")
	}

	var decoded Document
	if err := json.Unmarshal(buffer.Bytes(), &decoded); err != nil {
		t.Fatalf("streamed JSON did not parse: %v\n%s", err, buffer.String())
	}
	if decoded.Schema != Schema || decoded.GeneratedAt != "2026-07-15T12:00:00Z" {
		t.Fatalf("decoded metadata mismatch: %#v", decoded)
	}
	if decoded.GroupCount != 2 || decoded.FileCount != 5 || decoded.ReclaimableBytes != 4196 {
		t.Fatalf("decoded summary mismatch: %#v", decoded)
	}
	if decoded.Groups[0].Files[0].Path != `C:\docs\report.pdf` {
		t.Fatalf("decoded path mismatch: %q", decoded.Groups[0].Files[0].Path)
	}
}

func TestCSVGuardsFormulaInjection(t *testing.T) {
	document := BuildDocument(`C:\scanned`, sampleGroups(), time.Now())
	data, err := document.MarshalCSV()
	if err != nil {
		t.Fatalf("MarshalCSV failed: %v", err)
	}
	text := string(data)

	records, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil {
		t.Fatalf("CSV did not parse: %v", err)
	}
	if len(records) != 1+5 {
		t.Fatalf("expected header plus 5 rows, got %d records", len(records))
	}
	wantHeader := "generatedAt,scanFolder,group,sha256,groupSize,groupReclaimableBytes,reportReclaimableBytes,path,fileSize,createdAt,modifiedAt,category"
	if strings.Join(records[0], ",") != wantHeader {
		t.Fatalf("header = %q", strings.Join(records[0], ","))
	}
	if records[1][5] != "4096" || records[1][6] != "4196" {
		t.Fatalf("first group/report estimates = %q/%q", records[1][5], records[1][6])
	}
	if records[2][5] != "" || records[2][6] != "" || records[4][5] != "100" {
		t.Fatalf("estimate columns were not emitted once per scope: %#v", records)
	}
	if !strings.Contains(text, `,'=SUM(A1:A9).txt,`) {
		t.Fatal("formula-leading path was not neutralized")
	}
	if strings.Contains(text, "\n=") || strings.Contains(text, ",=") {
		t.Fatal("a cell still begins with a raw formula character")
	}
}

func TestWriteStreamsCSV(t *testing.T) {
	generatedAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	var buffer bytes.Buffer
	summary, err := Write(
		context.Background(),
		&buffer,
		FormatCSV,
		`C:\scanned`,
		sampleGroups(),
		generatedAt,
	)
	if err != nil {
		t.Fatalf("Write CSV failed: %v", err)
	}
	if summary.GroupCount != 2 || summary.FileCount != 5 || summary.ReclaimableBytes != 4196 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.BytesWritten != int64(buffer.Len()) {
		t.Fatalf("BytesWritten = %d, buffer length = %d", summary.BytesWritten, buffer.Len())
	}

	records, err := csv.NewReader(bytes.NewReader(buffer.Bytes())).ReadAll()
	if err != nil {
		t.Fatalf("streamed CSV did not parse: %v", err)
	}
	if len(records) != 6 {
		t.Fatalf("expected header plus 5 rows, got %d", len(records))
	}
	if records[1][5] != "4096" || records[1][6] != "4196" {
		t.Fatalf("first group/report estimates = %q/%q", records[1][5], records[1][6])
	}
	if records[4][7] != `'=SUM(A1:A9).txt` {
		t.Fatalf("formula-leading path = %q", records[4][7])
	}
}

func TestWriteHonorsCancellationBeforeOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buffer bytes.Buffer
	summary, err := Write(ctx, &buffer, FormatJSON, "", sampleGroups(), time.Now())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Write error = %v, want context.Canceled", err)
	}
	if summary.BytesWritten != 0 || buffer.Len() != 0 {
		t.Fatalf("canceled write emitted data: summary=%#v length=%d", summary, buffer.Len())
	}
}

func TestWritePropagatesDestinationFailure(t *testing.T) {
	wantErr := errors.New("destination failed")
	writer := &failingWriter{remaining: 32, err: wantErr}
	summary, err := Write(
		context.Background(),
		writer,
		FormatJSON,
		`C:\scanned`,
		sampleGroups(),
		time.Now(),
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Write error = %v, want %v", err, wantErr)
	}
	if summary.BytesWritten != 32 {
		t.Fatalf("BytesWritten = %d, want 32", summary.BytesWritten)
	}
}

func TestFormatExtension(t *testing.T) {
	if FormatCSV.Extension() != ".csv" || FormatJSON.Extension() != ".json" {
		t.Fatalf("extensions = %q and %q", FormatCSV.Extension(), FormatJSON.Extension())
	}
	if Format("xml").Extension() != "" {
		t.Fatalf("unsupported format extension = %q", Format("xml").Extension())
	}
}

func TestGuardSpreadsheetFormula(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"plain.txt":        "plain.txt",
		"=cmd":             "'=cmd",
		"+sum":             "'+sum",
		"-neg":             "'-neg",
		"@ref":             "'@ref",
		"\tlead":           "'\tlead",
		"\nlead":           "'\nlead",
		" =cmd":            "' =cmd",
		"\ufeff=cmd":       "'\ufeff=cmd",
		" plain.txt":       " plain.txt",
		`C:\normal\path.p`: `C:\normal\path.p`,
	}
	for input, expected := range cases {
		if got := guardSpreadsheetFormula(input); got != expected {
			t.Fatalf("guardSpreadsheetFormula(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestWriteFileAtomicCreatesAndReplacesReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.csv")
	bytesWritten, err := WriteFileAtomic(context.Background(), path, func(writer io.Writer) error {
		_, err := io.WriteString(writer, "first\r\n")
		return err
	})
	if err != nil {
		t.Fatalf("first WriteFileAtomic failed: %v", err)
	}
	if bytesWritten != 7 {
		t.Fatalf("first bytes written = %d, want 7", bytesWritten)
	}
	bytesWritten, err = WriteFileAtomic(context.Background(), path, func(writer io.Writer) error {
		_, err := io.WriteString(writer, "second\r\n")
		return err
	})
	if err != nil {
		t.Fatalf("replacement WriteFileAtomic failed: %v", err)
	}
	if bytesWritten != 8 {
		t.Fatalf("replacement bytes written = %d, want 8", bytesWritten)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "second\r\n" {
		t.Fatalf("report contents = %q", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "report.csv" {
		t.Fatalf("staging file survived successful write: %#v", entries)
	}
}

func TestWriteFileAtomicCallbackFailurePreservesTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("WriteFile setup failed: %v", err)
	}
	wantErr := errors.New("serialization failed")
	bytesWritten, err := WriteFileAtomic(context.Background(), path, func(writer io.Writer) error {
		if _, err := io.WriteString(writer, "partial"); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WriteFileAtomic error = %v, want %v", err, wantErr)
	}
	if bytesWritten != 7 {
		t.Fatalf("bytes written = %d, want 7", bytesWritten)
	}
	assertFileContents(t, path, "original")
	assertDirectoryNames(t, dir, "report.json")
}

func TestWriteFileAtomicCancellationPreservesTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("WriteFile setup failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	bytesWritten, err := WriteFileAtomic(ctx, path, func(writer io.Writer) error {
		if _, err := io.WriteString(writer, "partial"); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteFileAtomic error = %v, want context.Canceled", err)
	}
	if bytesWritten != 7 {
		t.Fatalf("bytes written = %d, want 7", bytesWritten)
	}
	assertFileContents(t, path, "original")
	assertDirectoryNames(t, dir, "report.json")
}

func TestWriteFileAtomicSupportsNearMaximumBasename(t *testing.T) {
	dir := t.TempDir()
	// The destination component is 250 characters. A staging pattern derived
	// from this basename would exceed the common 255-character component
	// limit, while the fixed short staging prefix remains safe.
	name := strings.Repeat("r", 246) + ".csv"
	path := filepath.Join(dir, name)
	_, err := WriteFileAtomic(context.Background(), path, func(writer io.Writer) error {
		_, err := io.WriteString(writer, "report\r\n")
		return err
	})
	if err != nil {
		t.Fatalf("WriteFileAtomic with %d-character basename failed: %v", len(name), err)
	}
	assertFileContents(t, path, "report\r\n")
	assertDirectoryNames(t, dir, name)
}

func TestWriteFileAtomicRejectsEmptyPath(t *testing.T) {
	if _, err := WriteFileAtomic(context.Background(), "", func(io.Writer) error { return nil }); err == nil {
		t.Fatal("empty report path was accepted")
	}
}

type failingWriter struct {
	remaining int
	err       error
}

func (w *failingWriter) Write(data []byte) (int, error) {
	if w.remaining == 0 {
		return 0, w.err
	}
	if len(data) <= w.remaining {
		w.remaining -= len(data)
		return len(data), nil
	}
	n := w.remaining
	w.remaining = 0
	return n, w.err
}

func assertFileContents(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", path, err)
	}
	if string(data) != expected {
		t.Fatalf("contents of %q = %q, want %q", path, data, expected)
	}
}

func assertDirectoryNames(t *testing.T, dir string, expected ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q) failed: %v", dir, err)
	}
	if len(entries) != len(expected) {
		t.Fatalf("directory %q has %d entries, want %d: %#v", dir, len(entries), len(expected), entries)
	}
	for index, name := range expected {
		if entries[index].Name() != name {
			t.Fatalf("directory entry %d = %q, want %q", index, entries[index].Name(), name)
		}
	}
}
