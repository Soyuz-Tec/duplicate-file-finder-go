//go:build windows

package gui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	"github.com/Soyuz-Tec/twintidy/internal/diagnostics"
	"github.com/Soyuz-Tec/twintidy/internal/report"
	"github.com/Soyuz-Tec/twintidy/internal/scanner"
)

// duplicateGroupsFromRows rebuilds the verified duplicate groups from the
// rows currently shown, so an export always matches what the user reviewed.
func duplicateGroupsFromRows(rows []duplicateRow) []scanner.DuplicateGroup {
	var groups []scanner.DuplicateGroup
	indexByGroup := map[int]int{}
	for _, row := range rows {
		if !row.Duplicate {
			continue
		}
		index, seen := indexByGroup[row.Group]
		if !seen {
			index = len(groups)
			indexByGroup[row.Group] = index
			groups = append(groups, scanner.DuplicateGroup{
				Size: row.File.Size,
				Hash: row.Hash,
			})
		}
		groups[index].Files = append(groups[index].Files, row.File)
	}
	return groups
}

func defaultReportFileName(now time.Time) string {
	return "TwinTidy-duplicates-" + now.Format("20060102-150405") + ".csv"
}

func reportFormatForFilter(filterIndex int) report.Format {
	if filterIndex == 2 {
		return report.FormatJSON
	}
	return report.FormatCSV
}

// resolveReportDestination treats the selected filter as authoritative and
// normalizes the final extension before any overwrite decision is made.
func resolveReportDestination(path string, filterIndex int) (string, report.Format, error) {
	if strings.TrimSpace(path) == "" {
		return "", "", errors.New("report path is empty")
	}

	format := reportFormatForFilter(filterIndex)
	desiredExtension := format.Extension()
	extension := filepath.Ext(path)
	if strings.EqualFold(extension, desiredExtension) {
		return path, format, nil
	}
	if extension == "" {
		return path + desiredExtension, format, nil
	}
	if strings.EqualFold(extension, report.FormatCSV.Extension()) || strings.EqualFold(extension, report.FormatJSON.Extension()) {
		return strings.TrimSuffix(path, extension) + desiredExtension, format, nil
	}
	return path + desiredExtension, format, nil
}

func pathsReferToSameDestination(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func resolvedReportDestinationExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, fmt.Errorf("report destination is a directory")
	}
	return true, nil
}

// confirmNormalizedReportOverwrite closes the gap where the native dialog
// checked the typed path but extension normalization selected another file.
func (a *windowsApp) confirmNormalizedReportOverwrite(dialogPath, finalPath string) bool {
	if pathsReferToSameDestination(dialogPath, finalPath) {
		return true
	}
	exists, err := resolvedReportDestinationExists(finalPath)
	if err != nil {
		walk.MsgBox(a.mw, "Export Failed", displayUntrustedText(err.Error()), walk.MsgBoxOK|walk.MsgBoxIconError)
		return false
	}
	if !exists {
		return true
	}

	message := "The selected format resolves to this existing file:\r\n\r\n" + displayFilesystemPath(finalPath) + "\r\n\r\nReplace that exact file with the new report?"
	return walk.MsgBox(a.mw, "Replace Existing Report", message, walk.MsgBoxYesNo|walk.MsgBoxIconWarning) == win.IDYES
}

type reportExportResult struct {
	summary report.Summary
	bytes   int64
	err     error
}

func writeReportFile(ctx context.Context, path string, format report.Format, folder string, groups []scanner.DuplicateGroup, generatedAt time.Time) reportExportResult {
	result := reportExportResult{}
	result.bytes, result.err = report.WriteFileAtomic(ctx, path, func(writer io.Writer) error {
		var err error
		result.summary, err = report.Write(ctx, writer, format, folder, groups, generatedAt)
		return err
	})
	return result
}

func (a *windowsApp) exportReport() {
	if a.operation.phase != phaseResultsReady {
		return
	}
	groups := duplicateGroupsFromRows(a.model.rows)
	if len(groups) == 0 {
		return
	}

	dialog := walk.FileDialog{
		Title:       "Export duplicate report",
		Filter:      "CSV report (*.csv)|*.csv|JSON report (*.json)|*.json",
		FilterIndex: 1,
		FilePath:    defaultReportFileName(time.Now()),
		Flags:       win.OFN_OVERWRITEPROMPT,
	}
	accepted, err := dialog.ShowSave(a.mw)
	if err != nil {
		walk.MsgBox(a.mw, "Export Failed", displayUntrustedText(err.Error()), walk.MsgBoxOK|walk.MsgBoxIconError)
		return
	}
	if !accepted || dialog.FilePath == "" {
		return
	}

	path, format, err := resolveReportDestination(dialog.FilePath, dialog.FilterIndex)
	if err != nil {
		walk.MsgBox(a.mw, "Export Failed", displayUntrustedText(err.Error()), walk.MsgBoxOK|walk.MsgBoxIconError)
		return
	}
	if !a.confirmNormalizedReportOverwrite(dialog.FilePath, path) {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	startedAt := time.Now()
	token, err := a.operation.beginExport(startedAt, cancel)
	if err != nil {
		cancel()
		diagnostics.Logf("report export start rejected: phase=%s error_type=%T", a.operation.phase, err)
		return
	}
	_ = a.statusLabel.SetText("Exporting the reviewed duplicate groups in the background.")
	a.renderFromPhase()
	diagnostics.Logf("report export started: generation=%d groups=%d format=%s", token.generation, len(groups), format)

	go func() {
		result := reportExportResult{}
		defer func() {
			fields := map[string]string{
				"generation":  fmt.Sprint(token.generation),
				"group_count": fmt.Sprint(len(groups)),
				"format":      string(format),
			}
			if crashErr := diagnostics.PanicToError("report export", recover(), fields); crashErr != nil {
				result.err = crashErr
			}
			a.synchronizeUI(func() {
				a.reportExportFinished(token, path, result)
			})
		}()

		result = writeReportFile(ctx, path, format, token.folder, groups, startedAt)
	}()
}

func (a *windowsApp) reportExportFinished(token operationToken, path string, result reportExportResult) {
	if !a.operation.accepts(token) {
		diagnostics.Logf("stale report export completion ignored: generation=%d folder_revision=%d", token.generation, token.folderRevision)
		return
	}
	accepted, shouldClose := a.operation.completeExport(token)
	if !accepted {
		return
	}
	a.renderFromPhase()
	if shouldClose {
		a.mw.Close()
		return
	}
	if errors.Is(result.err, context.Canceled) {
		diagnostics.Logf("report export canceled: generation=%d", token.generation)
		_ = a.statusLabel.SetText("Report export canceled. No partial report was kept.")
		return
	}
	if result.err != nil {
		diagnostics.Logf("report export failed: generation=%d error_type=%T", token.generation, result.err)
		_ = a.statusLabel.SetText("Report export failed. No partial report was kept.")
		walk.MsgBox(a.mw, "Export Failed", displayUntrustedText(result.err.Error()), walk.MsgBoxOK|walk.MsgBoxIconError)
		return
	}

	diagnostics.Logf("report exported: generation=%d groups=%d files=%d bytes=%d", token.generation, result.summary.GroupCount, result.summary.FileCount, result.bytes)
	_ = a.statusLabel.SetText(fmt.Sprintf("Exported %d duplicate group(s) covering %d file(s) to %s.", result.summary.GroupCount, result.summary.FileCount, displayFilesystemPath(path)))
}
