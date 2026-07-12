//go:build windows

package gui

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/Soyuz-Tec/duplicate-file-finder-go/internal/scanner"

	"github.com/lxn/walk"
	"github.com/lxn/win"
)

type previewMode uint8

const (
	previewModeSingle previewMode = iota + 1
	previewModeComparison
)

type previewRequest struct {
	ctx   context.Context
	token previewToken
	mode  previewMode
	rows  []duplicateRow
	dpi   int
}

type preparedPreview struct {
	token     previewToken
	mode      previewMode
	dpi       int
	details   string
	thumbnail image.Image
	pagePath  string
	tempDir   string
	err       error
}

type previewWorker struct {
	queue  *latestValueQueue[previewRequest]
	stop   chan struct{}
	closed atomic.Bool
}

func newPreviewWorker() *previewWorker {
	return &previewWorker{
		queue: newLatestValueQueue[previewRequest](),
		stop:  make(chan struct{}),
	}
}

func (a *windowsApp) startPreviewWorker() {
	if a.previewWorker != nil {
		return
	}
	a.previewWorker = newPreviewWorker()
	go a.runPreviewWorker(a.previewWorker)
}

func (a *windowsApp) stopPreviewWorker() {
	worker := a.previewWorker
	if worker == nil || worker.closed.Swap(true) {
		return
	}
	if a.previewCancel != nil {
		a.previewCancel()
		a.previewCancel = nil
	}
	a.previewState.invalidate()
	a.clearCurrentPreviewArtifact()
	close(worker.stop)
}

func (a *windowsApp) runPreviewWorker(worker *previewWorker) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr := win.CoInitializeEx(nil, win.COINIT_MULTITHREADED|win.COINIT_DISABLE_OLE1DDE)
	comReady := hr == win.S_OK || hr == win.S_FALSE
	if comReady {
		defer win.CoUninitialize()
	}

	for {
		select {
		case <-worker.stop:
			return
		case request := <-worker.queue.values():
			result := preparePreview(request, comReady)
			if errors.Is(result.err, context.Canceled) {
				cleanupPreviewArtifact(result.tempDir)
				continue
			}
			if worker.closed.Load() {
				cleanupPreviewArtifact(result.tempDir)
				continue
			}

			a.synchronizeUI(func() {
				if worker.closed.Load() {
					cleanupPreviewArtifact(result.tempDir)
					return
				}
				a.applyPreparedPreview(result)
			})
		}
	}
}

func preparePreview(request previewRequest, shellReady bool) (result preparedPreview) {
	result = preparedPreview{token: request.token, mode: request.mode, dpi: request.dpi}
	defer func() {
		if recover() != nil {
			cleanupPreviewArtifact(result.tempDir)
			result.pagePath = ""
			result.tempDir = ""
			result.err = errors.New("preview preparation failed")
		}
	}()

	if err := request.ctx.Err(); err != nil {
		result.err = err
		return result
	}

	switch request.mode {
	case previewModeSingle:
		return prepareSinglePreview(request, shellReady, result)
	case previewModeComparison:
		page, rendered, tempDir, err := buildComparisonPreviewPage(request.ctx, request.rows, maxComparisonPreviewFiles, shellReady)
		result.pagePath = page
		result.tempDir = tempDir
		result.err = err
		if err == nil {
			result.details = comparisonSummary(request.rows, rendered)
		}
		return result
	default:
		result.err = errors.New("unsupported preview mode")
		return result
	}
}

func prepareSinglePreview(request previewRequest, shellReady bool, result preparedPreview) preparedPreview {
	if len(request.rows) != 1 {
		result.err = errors.New("single preview requires one row")
		return result
	}

	row := request.rows[0]
	result.details = previewDetailsForRow(row)
	ext := strings.ToLower(filepath.Ext(row.File.Path))

	if shellReady {
		thumbnail, err := shellThumbnailForVerifiedRecord(row.File, 1200)
		if err == nil {
			result.thumbnail = thumbnail
			result.details += "\r\n\r\nExplorer-style thumbnail loaded through the Windows Shell provider. TwinTidy does not embed the original document or media file."
			return result
		}
	}

	if err := request.ctx.Err(); err != nil {
		result.err = err
		return result
	}

	if isTextExt(ext) {
		text, err := readVerifiedTextPreview(row.File)
		if err != nil {
			result.details += "\r\n\r\nText preview could not be prepared."
		} else {
			result.details += "\r\n\r\nText preview:\r\n" + text
		}
		return result
	}

	fallback := fallbackForRow(row)
	result.details += "\r\n\r\n" + fallback.Title + ":\r\n" + fallback.Text
	result.details += "\r\n\r\nThis is an app-generated fallback preview. Use Show In Explorer to inspect the file with an application you trust."
	return result
}

func shellThumbnailForVerifiedRecord(record scanner.FileRecord, size int) (image.Image, error) {
	file, err := scanner.OpenVerifiedRecordForRead(record)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	thumbnail, err := shellThumbnailNRGBAFromHeldRecordPath(record.Path, size)
	if err != nil {
		return nil, err
	}
	if err := scanner.ValidateRecordScope(record); err != nil {
		return nil, err
	}
	return thumbnail, nil
}

func (a *windowsApp) requestPreparedPreview(mode previewMode, rows []duplicateRow) {
	worker := a.previewWorker
	if worker == nil || worker.closed.Load() || len(rows) == 0 {
		return
	}

	a.invalidatePreview()
	token := a.previewState.begin(a.operation.folderRevision)
	ctx, cancel := context.WithCancel(context.Background())
	a.previewCancel = cancel

	rowSnapshot := snapshotPreviewRows(rows)
	request := previewRequest{
		ctx:   ctx,
		token: token,
		mode:  mode,
		rows:  rowSnapshot,
		dpi:   a.previewImage.DPI(),
	}

	a.setPreviewImage(nil)
	_ = a.previewText.SetText("Preparing preview...")
	worker.queue.submit(request)
}

func snapshotPreviewRows(rows []duplicateRow) []duplicateRow {
	return append([]duplicateRow(nil), rows...)
}

func (a *windowsApp) applyPreparedPreview(result preparedPreview) {
	if !a.previewState.accepts(result.token, a.operation.folderRevision) ||
		(a.operation.phase != phaseSurfaceReady && a.operation.phase != phaseResultsReady) {
		cleanupPreviewArtifact(result.tempDir)
		return
	}
	a.previewCancel = nil

	if result.err != nil {
		cleanupPreviewArtifact(result.tempDir)
		a.clearCurrentPreviewArtifact()
		a.setPreviewImage(nil)
		_ = a.previewText.SetText("Preview could not be prepared. The file may be unavailable, locked, or unsupported.")
		return
	}

	if result.mode == previewModeComparison {
		a.setPreviewImage(nil)
		a.previewImage.SetVisible(false)
		a.webPreview.SetVisible(true)
		_ = a.previewText.SetText(result.details)
		a.replaceCurrentPreviewArtifact(result.tempDir)
		a.allowedPreviewURL = previewArtifactFileURL(result.pagePath)
		if err := a.webPreview.SetURL(a.allowedPreviewURL); err != nil {
			a.clearCurrentPreviewArtifact()
			a.webPreview.SetVisible(false)
			a.previewImage.SetVisible(true)
			_ = a.previewText.SetText("Comparison preview could not be displayed.")
			return
		}
		return
	}

	a.clearCurrentPreviewArtifact()
	if result.thumbnail != nil {
		thumbnail, err := walk.NewBitmapFromImageForDPI(result.thumbnail, result.dpi)
		if err == nil {
			a.setPreviewImage(thumbnail)
		} else {
			a.setPreviewImage(nil)
			result.details += "\r\n\r\nVisual preview could not be displayed."
		}
	} else {
		a.setPreviewImage(nil)
	}
	_ = a.previewText.SetText(result.details)
}

func (a *windowsApp) invalidatePreview() {
	if a.previewCancel != nil {
		a.previewCancel()
		a.previewCancel = nil
	}
	a.previewState.invalidate()
	a.clearCurrentPreviewArtifact()
}

func (a *windowsApp) replaceCurrentPreviewArtifact(dir string) {
	if a.currentPreviewDir == dir {
		return
	}
	a.clearCurrentPreviewArtifact()
	a.currentPreviewDir = dir
}

func (a *windowsApp) clearCurrentPreviewArtifact() {
	dir := a.currentPreviewDir
	a.currentPreviewDir = ""
	a.allowedPreviewURL = ""
	cleanupPreviewArtifact(dir)
}

func cleanupPreviewArtifact(dir string) {
	if dir == "" {
		return
	}

	tempRoot, err := filepath.Abs(os.TempDir())
	if err != nil {
		return
	}
	target, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	relative, err := filepath.Rel(tempRoot, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return
	}
	if !strings.HasPrefix(filepath.Base(target), "twintidy-preview-") {
		return
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(target)
		return
	}
	resolvedTempRoot, err := filepath.EvalSymlinks(tempRoot)
	if err != nil {
		return
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return
	}
	resolvedTempRoot, err = filepath.Abs(resolvedTempRoot)
	if err != nil {
		return
	}
	resolvedTarget, err = filepath.Abs(resolvedTarget)
	if err != nil || !resolvedPreviewArtifactMatches(tempRoot, target, resolvedTempRoot, resolvedTarget) {
		_ = os.Remove(target)
		return
	}

	_ = os.RemoveAll(target)
}

func resolvedPreviewArtifactMatches(tempRoot, target, resolvedTempRoot, resolvedTarget string) bool {
	relative, err := filepath.Rel(tempRoot, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return false
	}
	expectedResolvedTarget := filepath.Join(resolvedTempRoot, relative)
	return strings.EqualFold(filepath.Clean(expectedResolvedTarget), filepath.Clean(resolvedTarget))
}
