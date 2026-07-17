// Package report serializes verified duplicate-scan results into local CSV
// and JSON documents. A report is a read-only record of what the user
// reviewed: it grants no authority, is written only where the user chooses,
// and never leaves the computer on its own.
package report

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Soyuz-Tec/twintidy/internal/scanner"
)

// Schema identifies the JSON document layout.
const Schema = "twintidy.duplicate-report/v1"

// Format identifies a supported report serialization.
type Format string

const (
	// FormatCSV emits one spreadsheet-safe row per duplicate file.
	FormatCSV Format = "csv"
	// FormatJSON emits the canonical duplicate-report JSON document.
	FormatJSON Format = "json"
)

// Extension returns the conventional file extension for the format.
// Unsupported formats return an empty string.
func (f Format) Extension() string {
	switch f {
	case FormatCSV:
		return ".csv"
	case FormatJSON:
		return ".json"
	default:
		return ""
	}
}

// Summary describes the report content and the successfully streamed byte
// count. BytesWritten can be non-zero with an error when a destination fails
// partway through a write.
type Summary struct {
	GroupCount       int
	FileCount        int
	ReclaimableBytes int64
	BytesWritten     int64
}

// File is one member of an exported duplicate group.
type File struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	CreatedAt  string `json:"createdAt,omitempty"`
	ModifiedAt string `json:"modifiedAt,omitempty"`
	Category   string `json:"category,omitempty"`
}

// Group is one verified duplicate group: files whose complete SHA-256
// content matched at scan time.
type Group struct {
	Size  int64  `json:"size"`
	Hash  string `json:"sha256"`
	Files []File `json:"files"`
}

// Document is the exported report.
type Document struct {
	Schema           string  `json:"schema"`
	GeneratedAt      string  `json:"generatedAt"`
	Folder           string  `json:"folder,omitempty"`
	GroupCount       int     `json:"groupCount"`
	FileCount        int     `json:"fileCount"`
	ReclaimableBytes int64   `json:"reclaimableBytes"`
	Groups           []Group `json:"groups"`
}

// BuildDocument converts scan results into a Document. Group and file order
// is preserved so the export matches what the user reviewed on screen.
// ReclaimableBytes is the planning estimate of keeping one copy per group;
// it asserts nothing about what any future cleanup would actually do.
func BuildDocument(folder string, groups []scanner.DuplicateGroup, generatedAt time.Time) Document {
	document := Document{
		Schema:      Schema,
		GeneratedAt: generatedAt.UTC().Format(time.RFC3339),
		Folder:      folder,
		Groups:      make([]Group, 0, len(groups)),
	}
	for _, group := range groups {
		exported := Group{
			Size:  group.Size,
			Hash:  group.Hash,
			Files: make([]File, 0, len(group.Files)),
		}
		for _, file := range group.Files {
			exported.Files = append(exported.Files, fileFromRecord(file))
		}
		document.Groups = append(document.Groups, exported)
		document.FileCount += len(exported.Files)
		if extra := int64(len(exported.Files)) - 1; extra > 0 {
			document.ReclaimableBytes += extra * group.Size
		}
	}
	document.GroupCount = len(document.Groups)
	return document
}

// MarshalJSONDocument renders the canonical indented JSON form.
func (d Document) MarshalJSONDocument() ([]byte, error) {
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// MarshalCSV renders one row per exported file. Cell values that a
// spreadsheet would evaluate as formulas are prefixed with an apostrophe so
// an adversarial file name cannot execute when the report is opened. Summary
// estimates appear only on the first applicable row so column totals do not
// double-count a group or the complete report.
func (d Document) MarshalCSV() ([]byte, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	writer.UseCRLF = true

	if err := writer.Write(csvHeader); err != nil {
		return nil, err
	}
	reportTotalWritten := false
	for index, group := range d.Groups {
		groupReclaimable := reclaimableBytes(group.Size, len(group.Files))
		for fileIndex, file := range group.Files {
			groupEstimate := ""
			if fileIndex == 0 {
				groupEstimate = strconv.FormatInt(groupReclaimable, 10)
			}
			reportEstimate := ""
			if !reportTotalWritten {
				reportEstimate = strconv.FormatInt(d.ReclaimableBytes, 10)
				reportTotalWritten = true
			}
			if err := writer.Write(csvRow(
				d.GeneratedAt,
				d.Folder,
				index,
				group.Size,
				group.Hash,
				file,
				groupEstimate,
				reportEstimate,
			)); err != nil {
				return nil, err
			}
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Write streams a report directly to w without first retaining the complete
// serialized document in memory. It checks ctx while counting, between rows,
// and around destination writes so cancellation stops large exports promptly.
func Write(
	ctx context.Context,
	w io.Writer,
	format Format,
	folder string,
	groups []scanner.DuplicateGroup,
	generatedAt time.Time,
) (Summary, error) {
	if ctx == nil {
		return Summary{}, errors.New("report context is nil")
	}
	if w == nil {
		return Summary{}, errors.New("report writer is nil")
	}
	if format.Extension() == "" {
		return Summary{}, fmt.Errorf("unsupported report format %q", format)
	}

	summary, err := summarize(ctx, groups)
	if err != nil {
		return summary, err
	}
	counted := &contextWriter{ctx: ctx, writer: w}
	switch format {
	case FormatCSV:
		err = writeCSV(ctx, counted, folder, groups, generatedAt, summary)
	case FormatJSON:
		err = writeJSON(ctx, counted, folder, groups, generatedAt, summary)
	}
	summary.BytesWritten = counted.bytesWritten
	return summary, err
}

// WriteFileAtomic creates a short-named staging file beside path, invokes
// write to populate it, syncs and closes the complete file, then renames it
// into place. Any callback, I/O, or cancellation failure removes the staging
// file and preserves an existing destination unchanged.
func WriteFileAtomic(
	ctx context.Context,
	path string,
	write func(io.Writer) error,
) (int64, error) {
	if ctx == nil {
		return 0, errors.New("report context is nil")
	}
	if path == "" {
		return 0, errors.New("report path is empty")
	}
	if write == nil {
		return 0, errors.New("report write callback is nil")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	dir := filepath.Dir(path)
	staging, err := os.CreateTemp(dir, ".twintidy-report-*")
	if err != nil {
		return 0, err
	}
	stagingPath := staging.Name()
	closed := false
	renamed := false
	defer func() {
		if !closed {
			_ = staging.Close()
		}
		if !renamed {
			_ = os.Remove(stagingPath)
		}
	}()

	counted := &contextWriter{ctx: ctx, writer: staging}
	if err := write(counted); err != nil {
		return counted.bytesWritten, err
	}
	if err := ctx.Err(); err != nil {
		return counted.bytesWritten, err
	}
	if err := staging.Sync(); err != nil {
		return counted.bytesWritten, err
	}
	if err := ctx.Err(); err != nil {
		return counted.bytesWritten, err
	}
	if err := staging.Close(); err != nil {
		return counted.bytesWritten, err
	}
	closed = true
	if err := ctx.Err(); err != nil {
		return counted.bytesWritten, err
	}
	if err := os.Rename(stagingPath, path); err != nil {
		return counted.bytesWritten, err
	}
	renamed = true
	return counted.bytesWritten, nil
}

var csvHeader = []string{
	"generatedAt",
	"scanFolder",
	"group",
	"sha256",
	"groupSize",
	"groupReclaimableBytes",
	"reportReclaimableBytes",
	"path",
	"fileSize",
	"createdAt",
	"modifiedAt",
	"category",
}

func summarize(ctx context.Context, groups []scanner.DuplicateGroup) (Summary, error) {
	summary := Summary{GroupCount: len(groups)}
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		summary.FileCount += len(group.Files)
		summary.ReclaimableBytes += reclaimableBytes(group.Size, len(group.Files))
	}
	return summary, nil
}

func reclaimableBytes(size int64, fileCount int) int64 {
	if extra := int64(fileCount) - 1; extra > 0 {
		return extra * size
	}
	return 0
}

func writeCSV(
	ctx context.Context,
	w io.Writer,
	folder string,
	groups []scanner.DuplicateGroup,
	generatedAt time.Time,
	summary Summary,
) error {
	writer := csv.NewWriter(w)
	writer.UseCRLF = true
	if err := writer.Write(csvHeader); err != nil {
		return err
	}

	reportTotalWritten := false
	formattedGeneratedAt := generatedAt.UTC().Format(time.RFC3339)
	for groupIndex, group := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		groupReclaimable := reclaimableBytes(group.Size, len(group.Files))
		for fileIndex, record := range group.Files {
			if err := ctx.Err(); err != nil {
				return err
			}
			groupEstimate := ""
			if fileIndex == 0 {
				groupEstimate = strconv.FormatInt(groupReclaimable, 10)
			}
			reportEstimate := ""
			if !reportTotalWritten {
				reportEstimate = strconv.FormatInt(summary.ReclaimableBytes, 10)
				reportTotalWritten = true
			}
			if err := writer.Write(csvRow(
				formattedGeneratedAt,
				folder,
				groupIndex,
				group.Size,
				group.Hash,
				fileFromRecord(record),
				groupEstimate,
				reportEstimate,
			)); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}

func csvRow(
	generatedAt string,
	folder string,
	groupIndex int,
	groupSize int64,
	hash string,
	file File,
	groupEstimate string,
	reportEstimate string,
) []string {
	return []string{
		guardSpreadsheetFormula(generatedAt),
		guardSpreadsheetFormula(folder),
		strconv.Itoa(groupIndex + 1),
		guardSpreadsheetFormula(hash),
		strconv.FormatInt(groupSize, 10),
		groupEstimate,
		reportEstimate,
		guardSpreadsheetFormula(file.Path),
		strconv.FormatInt(file.Size, 10),
		guardSpreadsheetFormula(file.CreatedAt),
		guardSpreadsheetFormula(file.ModifiedAt),
		guardSpreadsheetFormula(file.Category),
	}
}

func writeJSON(
	ctx context.Context,
	w io.Writer,
	folder string,
	groups []scanner.DuplicateGroup,
	generatedAt time.Time,
	summary Summary,
) error {
	if err := writeString(w, "{\n  \"schema\": "); err != nil {
		return err
	}
	if err := writeJSONValue(w, Schema); err != nil {
		return err
	}
	if err := writeString(w, ",\n  \"generatedAt\": "); err != nil {
		return err
	}
	if err := writeJSONValue(w, generatedAt.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if folder != "" {
		if err := writeString(w, ",\n  \"folder\": "); err != nil {
			return err
		}
		if err := writeJSONValue(w, folder); err != nil {
			return err
		}
	}
	if err := writeString(w, ",\n  \"groupCount\": "+strconv.Itoa(summary.GroupCount)); err != nil {
		return err
	}
	if err := writeString(w, ",\n  \"fileCount\": "+strconv.Itoa(summary.FileCount)); err != nil {
		return err
	}
	if err := writeString(w, ",\n  \"reclaimableBytes\": "+strconv.FormatInt(summary.ReclaimableBytes, 10)); err != nil {
		return err
	}
	if err := writeString(w, ",\n  \"groups\": ["); err != nil {
		return err
	}

	for groupIndex, group := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		separator := "\n"
		if groupIndex > 0 {
			separator = ",\n"
		}
		if err := writeString(w, separator+"    {\n      \"size\": "+strconv.FormatInt(group.Size, 10)+",\n      \"sha256\": "); err != nil {
			return err
		}
		if err := writeJSONValue(w, group.Hash); err != nil {
			return err
		}
		if err := writeString(w, ",\n      \"files\": ["); err != nil {
			return err
		}
		for fileIndex, record := range group.Files {
			if err := ctx.Err(); err != nil {
				return err
			}
			fileSeparator := "\n"
			if fileIndex > 0 {
				fileSeparator = ",\n"
			}
			if err := writeString(w, fileSeparator+"        "); err != nil {
				return err
			}
			if err := writeJSONValue(w, fileFromRecord(record)); err != nil {
				return err
			}
		}
		if len(group.Files) > 0 {
			if err := writeString(w, "\n      "); err != nil {
				return err
			}
		}
		if err := writeString(w, "]\n    }"); err != nil {
			return err
		}
	}
	if len(groups) > 0 {
		if err := writeString(w, "\n  "); err != nil {
			return err
		}
	}
	return writeString(w, "]\n}\n")
}

func writeJSONValue(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func writeString(w io.Writer, value string) error {
	n, err := io.WriteString(w, value)
	if err != nil {
		return err
	}
	if n != len(value) {
		return io.ErrShortWrite
	}
	return nil
}

type contextWriter struct {
	ctx          context.Context
	writer       io.Writer
	bytesWritten int64
}

func (w *contextWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(data)
	w.bytesWritten += int64(n)
	if err != nil {
		return n, err
	}
	if n != len(data) {
		return n, io.ErrShortWrite
	}
	if err := w.ctx.Err(); err != nil {
		return n, err
	}
	return n, nil
}

func fileFromRecord(file scanner.FileRecord) File {
	return File{
		Path:       file.Path,
		Size:       file.Size,
		CreatedAt:  formatTimestamp(file.CreatedAt),
		ModifiedAt: formatTimestamp(file.ModifiedAt),
		Category:   string(file.Category),
	}
}

func formatTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// guardSpreadsheetFormula neutralizes cells that Excel-compatible software
// would interpret as formulas or command triggers.
func guardSpreadsheetFormula(value string) string {
	if value == "" {
		return value
	}
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\ufeff'
	})
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return "'" + value
	}
	switch value[0] {
	case '\t', '\r', '\n':
		return "'" + value
	}
	return value
}
