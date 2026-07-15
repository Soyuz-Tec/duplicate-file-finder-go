//go:build windows

package gui

import (
	"context"
	"errors"
	"fmt"
	"html"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Soyuz-Tec/duplicate-file-finder-go/internal/buildinfo"
	"github.com/Soyuz-Tec/duplicate-file-finder-go/internal/diagnostics"
	"github.com/Soyuz-Tec/duplicate-file-finder-go/internal/scanner"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
)

const previewLimitBytes int64 = 64 * 1024
const maxComparisonPreviewFiles = 12

var (
	macBackground    = walk.RGB(245, 246, 248)
	macToolbarTop    = walk.RGB(252, 253, 255)
	macToolbarBottom = walk.RGB(239, 242, 246)
	macPanel         = walk.RGB(251, 252, 254)
	macPanelAlt      = walk.RGB(247, 249, 252)
	macInk           = walk.RGB(29, 29, 31)
	macSecondaryInk  = walk.RGB(103, 108, 118)
	macMutedInk      = walk.RGB(132, 138, 148)
	macAccentBlue    = walk.RGB(0, 122, 255)
	macDangerRed     = walk.RGB(255, 59, 48)
)

func macBodyFont() Font {
	return Font{Family: "Segoe UI Variable Text", PointSize: 9}
}

func macControlFont() Font {
	return Font{Family: "Segoe UI Variable Text", PointSize: 9}
}

func macTitleFont() Font {
	return Font{Family: "Segoe UI Variable Display", PointSize: 13, Bold: true}
}

func macSectionFont() Font {
	return Font{Family: "Segoe UI Variable Text", PointSize: 9, Bold: true}
}

type windowsApp struct {
	mw *walk.MainWindow

	selectFolderButton  *walk.PushButton
	scanButton          *walk.PushButton
	cancelButton        *walk.PushButton
	clearButton         *walk.PushButton
	resetButton         *walk.PushButton
	logButton           *walk.PushButton
	keepNewestButton    *walk.PushButton
	keepOldestButton    *walk.PushButton
	clearSelectButton   *walk.PushButton
	openButton          *walk.PushButton
	previewSafetyButton *walk.PushButton
	deleteButton        *walk.PushButton

	folderLabel  *walk.Label
	stageLabel   *walk.Label
	filesLabel   *walk.Label
	elapsedLabel *walk.Label
	currentLabel *walk.Label
	statusLabel  *walk.Label
	progress     *walk.ProgressBar
	table        *walk.TableView
	previewImage *walk.ImageView
	webPreview   *walk.WebView
	previewText  *walk.TextEdit
	focusLabel   *walk.Label

	pdfCheck        *walk.CheckBox
	textCheck       *walk.CheckBox
	wordCheck       *walk.CheckBox
	excelCheck      *walk.CheckBox
	powerPointCheck *walk.CheckBox
	imagesCheck     *walk.CheckBox
	audioCheck      *walk.CheckBox
	videoCheck      *walk.CheckBox
	archivesCheck   *walk.CheckBox
	otherCheck      *walk.CheckBox

	engine              *scanner.Engine
	model               *duplicateTableModel
	currentPreviewImage walk.Image

	operation     operationState
	surfaceReport scanner.SurfaceReport

	previewState      previewGenerationState
	previewWorker     *previewWorker
	previewCancel     context.CancelFunc
	currentPreviewDir string
	allowedPreviewURL string
	uiClosing         atomic.Bool
}

type duplicateRow struct {
	Number    int
	Group     int
	Key       string
	Hash      string
	Status    string
	Duplicate bool
	Selected  bool
	File      scanner.FileRecord
}

type duplicateTableModel struct {
	walk.TableModelBase
	rows               []duplicateRow
	onSelectionChanged func()
}

func Run() error {
	diagnostics.Logf("gui initializing")
	ui := &windowsApp{
		engine:    scanner.NewEngine(0),
		model:     &duplicateTableModel{},
		operation: newOperationState(),
	}
	ui.model.onSelectionChanged = ui.updateDeleteActionState
	if err := ui.create(); err != nil {
		diagnostics.Logf("gui create failed: error_type=%T", err)
		return fmt.Errorf("create TwinTidy window: %w", err)
	}
	ui.startPreviewWorker()
	defer ui.stopPreviewWorker()
	ui.mw.Closing().Attach(ui.handleWindowClosing)
	ui.setInitialState()
	diagnostics.Logf("gui ready")
	ui.mw.Run()
	return nil
}

func SmokeTest() error {
	ui := &windowsApp{
		engine:    scanner.NewEngine(1),
		model:     &duplicateTableModel{},
		operation: newOperationState(),
	}
	if err := ui.create(); err != nil {
		return fmt.Errorf("create TwinTidy smoke-test window: %w", err)
	}
	ui.mw.Synchronize(func() {
		_ = ui.mw.Close()
	})
	if exitCode := ui.mw.Run(); exitCode != 0 {
		return fmt.Errorf("smoke-test window message loop exited with code %d", exitCode)
	}
	return nil
}

func (a *windowsApp) synchronizeUI(callback func()) {
	if a.uiClosing.Load() {
		return
	}
	a.mw.Synchronize(func() {
		if a.uiClosing.Load() {
			return
		}
		callback()
	})
}

func (a *windowsApp) handleWindowClosing(canceled *bool, _ walk.CloseReason) {
	disposition := a.operation.requestClose()
	a.stopPreviewWorker()
	if disposition == closeDeferred {
		*canceled = true
		_ = a.statusLabel.SetText("Finishing the active Recycle Bin operation before closing TwinTidy.")
		a.renderFromPhase()
		generation := uint64(0)
		if a.operation.active != nil {
			generation = a.operation.active.generation
		}
		diagnostics.Logf("window close deferred: generation=%d", generation)
		return
	}
	a.uiClosing.Store(true)
}

func (a *windowsApp) create() error {
	return MainWindow{
		AssignTo: &a.mw,
		Title:    "TwinTidy - Safe Duplicate File Review",
		Size:     Size{Width: 1440, Height: 860},
		MinSize:  Size{Width: 1120, Height: 700},
		Font:     macBodyFont(),
		Background: SolidColorBrush{
			Color: macBackground,
		},
		Layout: VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}, Spacing: 10},
		Children: []Widget{
			GradientComposite{
				Color1:   macToolbarTop,
				Color2:   macToolbarBottom,
				Vertical: true,
				Layout:   HBox{Margins: Margins{Left: 12, Top: 9, Right: 12, Bottom: 9}, Spacing: 8},
				Children: []Widget{
					Label{Text: "TwinTidy", Font: macTitleFont(), TextColor: macInk, MinSize: Size{Width: 190}},
					HSpacer{},
					PushButton{AssignTo: &a.selectFolderButton, Text: "Select Folder", Font: macControlFont(), OnClicked: a.selectFolder},
					PushButton{AssignTo: &a.scanButton, Text: "Scan", Font: macControlFont(), Background: SolidColorBrush{Color: macAccentBlue}, OnClicked: a.startScan},
					PushButton{AssignTo: &a.cancelButton, Text: "Cancel", Font: macControlFont(), OnClicked: a.cancelScan},
					PushButton{AssignTo: &a.clearButton, Text: "Clear Results", Font: macControlFont(), OnClicked: a.clearResults},
					PushButton{AssignTo: &a.resetButton, Text: "Reset", Font: macControlFont(), OnClicked: a.resetAll},
					PushButton{AssignTo: &a.logButton, Text: "Open Logs", Font: macControlFont(), OnClicked: a.openLogs},
					PushButton{Text: "About", Font: macControlFont(), OnClicked: a.showAbout},
				},
			},
			Label{AssignTo: &a.folderLabel, Text: "No folder selected", TextColor: macSecondaryInk, Font: macBodyFont()},
			GroupBox{
				Title: "Scan Status",
				Font:  macSectionFont(),
				Background: SolidColorBrush{
					Color: macPanel,
				},
				Layout: VBox{Margins: Margins{Left: 10, Top: 10, Right: 10, Bottom: 10}, Spacing: 7},
				Children: []Widget{
					ProgressBar{AssignTo: &a.progress, MinValue: 0, MaxValue: 1000, Value: 0, Background: SolidColorBrush{Color: macPanelAlt}},
					Composite{
						Background: SolidColorBrush{Color: macPanel},
						Layout:     Grid{Columns: 8, MarginsZero: true, Spacing: 6},
						Children: []Widget{
							Label{Text: "Stage", TextColor: macMutedInk},
							Label{AssignTo: &a.stageLabel, Text: "Idle", TextColor: macInk},
							Label{Text: "Files", TextColor: macMutedInk},
							Label{AssignTo: &a.filesLabel, Text: "0 files", TextColor: macInk},
							Label{Text: "Elapsed", TextColor: macMutedInk},
							Label{AssignTo: &a.elapsedLabel, Text: "0s", TextColor: macInk},
							Label{Text: "Current", TextColor: macMutedInk},
							Label{AssignTo: &a.currentLabel, Text: "-", TextColor: macInk},
						},
					},
					Label{AssignTo: &a.statusLabel, Text: "Select a folder to start.", TextColor: macSecondaryInk},
				},
			},
			GroupBox{
				Title: "File Focus",
				Font:  macSectionFont(),
				Background: SolidColorBrush{
					Color: macPanel,
				},
				Layout: VBox{Margins: Margins{Left: 10, Top: 8, Right: 10, Bottom: 8}, Spacing: 6},
				Children: []Widget{
					Composite{
						Background: SolidColorBrush{Color: macPanel},
						Layout:     Grid{Columns: 5, MarginsZero: true, Spacing: 8},
						Children: []Widget{
							CheckBox{AssignTo: &a.pdfCheck, Text: "PDF", Checked: true, Font: macControlFont(), Background: SolidColorBrush{Color: macPanel}},
							CheckBox{AssignTo: &a.textCheck, Text: "Text", Checked: true, Font: macControlFont(), Background: SolidColorBrush{Color: macPanel}},
							CheckBox{AssignTo: &a.wordCheck, Text: "Word", Checked: true, Font: macControlFont(), Background: SolidColorBrush{Color: macPanel}},
							CheckBox{AssignTo: &a.excelCheck, Text: "Excel", Checked: true, Font: macControlFont(), Background: SolidColorBrush{Color: macPanel}},
							CheckBox{AssignTo: &a.powerPointCheck, Text: "PowerPoint", Checked: true, Font: macControlFont(), Background: SolidColorBrush{Color: macPanel}},
							CheckBox{AssignTo: &a.imagesCheck, Text: "Images", Checked: true, Font: macControlFont(), Background: SolidColorBrush{Color: macPanel}},
							CheckBox{AssignTo: &a.audioCheck, Text: "Audio", Checked: true, Font: macControlFont(), Background: SolidColorBrush{Color: macPanel}},
							CheckBox{AssignTo: &a.videoCheck, Text: "Video", Checked: true, Font: macControlFont(), Background: SolidColorBrush{Color: macPanel}},
							CheckBox{AssignTo: &a.archivesCheck, Text: "Archives", Checked: true, Font: macControlFont(), Background: SolidColorBrush{Color: macPanel}},
							CheckBox{AssignTo: &a.otherCheck, Text: "Other", Checked: true, Font: macControlFont(), Background: SolidColorBrush{Color: macPanel}},
						},
					},
					Label{AssignTo: &a.focusLabel, Text: "Run a surface scan to enable file-type focus. System, app, executable, and dependency files are skipped automatically.", TextColor: macSecondaryInk},
				},
			},
			Composite{
				Background: SolidColorBrush{Color: macBackground},
				Layout:     HBox{MarginsZero: true, Spacing: 8},
				Children: []Widget{
					PushButton{AssignTo: &a.keepNewestButton, Text: "Keep Newest", Font: macControlFont(), OnClicked: a.selectAllExceptNewest},
					PushButton{AssignTo: &a.keepOldestButton, Text: "Keep Oldest", Font: macControlFont(), OnClicked: a.selectAllExceptOldest},
					PushButton{AssignTo: &a.clearSelectButton, Text: "Clear Selection", Font: macControlFont(), OnClicked: a.clearSelection},
					PushButton{AssignTo: &a.openButton, Text: "Show In Explorer", Font: macControlFont(), OnClicked: a.showSelectedInExplorer},
					PushButton{AssignTo: &a.previewSafetyButton, Text: "Preview Safety", Font: macControlFont(), OnClicked: a.showPreviewSafety},
					HSpacer{},
					PushButton{AssignTo: &a.deleteButton, Text: "Recycle Checked", Font: macControlFont(), Background: SolidColorBrush{Color: macDangerRed}, OnClicked: a.confirmDeleteSelected},
				},
			},
			HSplitter{
				Background: SolidColorBrush{Color: macBackground},
				Children: []Widget{
					TableView{
						AssignTo:                 &a.table,
						Model:                    a.model,
						Background:               SolidColorBrush{Color: walk.RGB(255, 255, 255)},
						Font:                     macBodyFont(),
						CheckBoxes:               true,
						AlternatingRowBG:         true,
						ColumnsOrderable:         true,
						ColumnsSizable:           true,
						LastColumnStretched:      true,
						MultiSelection:           true,
						OnCurrentIndexChanged:    a.updatePreviewFromSelection,
						OnSelectedIndexesChanged: a.updatePreviewFromSelection,
						OnMouseUp:                func(_, _ int, _ walk.MouseButton) { a.updateDeleteActionState() },
						OnKeyUp:                  func(_ walk.Key) { a.updateDeleteActionState() },
						Columns: []TableViewColumn{
							{Title: "No.", Width: 52},
							{Title: "Status", Width: 105},
							{Title: "Group", Width: 62},
							{Title: "Name", Width: 230},
							{Title: "Size", Width: 95},
							{Title: "Type", Width: 95},
							{Title: "Modified", Width: 132},
							{Title: "Created", Width: 132},
							{Title: "Hash", Width: 96},
							{Title: "Path", Width: 420},
						},
					},
					GroupBox{
						Title: "Preview And Verification",
						Font:  macSectionFont(),
						Background: SolidColorBrush{
							Color: macPanel,
						},
						Layout: VBox{Margins: Margins{Left: 10, Top: 10, Right: 10, Bottom: 10}, Spacing: 8},
						Children: []Widget{
							ImageView{
								AssignTo:      &a.previewImage,
								Background:    SolidColorBrush{Color: walk.RGB(255, 255, 255)},
								Mode:          ImageViewModeShrink,
								Margin:        12,
								MinSize:       Size{Width: 500, Height: 430},
								StretchFactor: 4,
							},
							WebView{
								AssignTo:                 &a.webPreview,
								Background:               SolidColorBrush{Color: walk.RGB(255, 255, 255)},
								Visible:                  false,
								MinSize:                  Size{Width: 500, Height: 430},
								StretchFactor:            4,
								NativeContextMenuEnabled: false,
								ShortcutsEnabled:         false,
								OnNavigating:             a.guardPreviewNavigation,
								OnNewWindow: func(event *walk.WebViewNewWindowEventData) {
									event.SetCanceled(true)
								},
							},
							TextEdit{
								AssignTo:      &a.previewText,
								Background:    SolidColorBrush{Color: walk.RGB(255, 255, 255)},
								Font:          macBodyFont(),
								TextColor:     macInk,
								ReadOnly:      true,
								VScroll:       true,
								HScroll:       true,
								MinSize:       Size{Width: 500, Height: 145},
								StretchFactor: 1,
							},
						},
					},
				},
			},
		},
	}.Create()
}

func (a *windowsApp) setInitialState() {
	a.clearFileFocus()
	if scanner.RecycleSupported() {
		a.setPreviewMessage("Select a duplicate row to show an Explorer-style visual preview and verification details.")
	} else {
		_ = a.deleteButton.SetText("Recycle Unavailable")
		a.setPreviewMessage("Safe cleanup is disabled in this pre-release build. Scanning, exact-match verification, selection, and previews remain available; no file will be recycled.")
	}
	a.renderFromPhase()
}

type phaseControls struct {
	selectFolder   bool
	scan           bool
	cancel         bool
	clear          bool
	reset          bool
	fileFocus      bool
	table          bool
	resultActions  bool
	deleteSelected bool
	scanText       string
}

func controlsForOperation(state *operationState, hasRows, hasDuplicates bool, checkedCount int) phaseControls {
	reviewable := state.phase == phaseSurfaceReady || state.phase == phaseResultsReady
	resultActions := state.phase == phaseResultsReady && hasDuplicates
	scanText := "Surface Scan"
	switch state.phase {
	case phaseSurfaceReady, phaseDuplicateScanning, phaseDuplicateCancelling, phaseResultsReady, phaseDeleting, phaseClosingAfterDelete:
		scanText = "Find Duplicates"
	}

	return phaseControls{
		selectFolder:   state.canChangeFolder(),
		scan:           state.phase == phaseFolderReady || state.phase == phaseSurfaceReady,
		cancel:         state.phase == phaseSurfaceScanning || state.phase == phaseDuplicateScanning,
		clear:          reviewable && hasRows,
		reset:          state.canReset(),
		fileFocus:      reviewable,
		table:          reviewable,
		resultActions:  resultActions,
		deleteSelected: resultActions && checkedCount > 0,
		scanText:       scanText,
	}
}

func (a *windowsApp) renderFromPhase() {
	hasRows := len(a.model.rows) > 0
	hasDuplicates := rowsContainDuplicates(a.model.rows)
	controls := controlsForOperation(&a.operation, hasRows, hasDuplicates, a.model.selectedCount())

	a.selectFolderButton.SetEnabled(controls.selectFolder)
	_ = a.scanButton.SetText(controls.scanText)
	a.scanButton.SetEnabled(controls.scan)
	a.cancelButton.SetEnabled(controls.cancel)
	a.clearButton.SetEnabled(controls.clear)
	a.resetButton.SetEnabled(controls.reset)
	a.setFileFocusEnabled(controls.fileFocus)
	a.table.SetEnabled(controls.table)
	a.keepNewestButton.SetEnabled(controls.resultActions)
	a.keepOldestButton.SetEnabled(controls.resultActions)
	a.clearSelectButton.SetEnabled(controls.resultActions)
	a.deleteButton.SetEnabled(controls.deleteSelected && scanner.RecycleSupported())

	currentIndex := a.table.CurrentIndex()
	hasCurrentRow := controls.table && currentIndex >= 0 && currentIndex < len(a.model.rows)
	a.openButton.SetEnabled(hasCurrentRow)
	a.previewSafetyButton.SetEnabled(hasCurrentRow)
}

func rowsContainDuplicates(rows []duplicateRow) bool {
	for _, row := range rows {
		if row.Duplicate {
			return true
		}
	}
	return false
}

func (a *windowsApp) categoryChecks() []categoryCheck {
	return []categoryCheck{
		{category: scanner.CategoryPDF, label: "PDF", check: a.pdfCheck},
		{category: scanner.CategoryText, label: "Text", check: a.textCheck},
		{category: scanner.CategoryWord, label: "Word", check: a.wordCheck},
		{category: scanner.CategoryExcel, label: "Excel", check: a.excelCheck},
		{category: scanner.CategoryPowerPoint, label: "PowerPoint", check: a.powerPointCheck},
		{category: scanner.CategoryImages, label: "Images", check: a.imagesCheck},
		{category: scanner.CategoryAudio, label: "Audio", check: a.audioCheck},
		{category: scanner.CategoryVideo, label: "Video", check: a.videoCheck},
		{category: scanner.CategoryArchives, label: "Archives", check: a.archivesCheck},
		{category: scanner.CategoryOther, label: "Other", check: a.otherCheck},
	}
}

func (a *windowsApp) clearFileFocus() {
	for _, item := range a.categoryChecks() {
		if item.check == nil {
			continue
		}
		_ = item.check.SetText(item.label)
		item.check.SetChecked(true)
		item.check.SetEnabled(false)
	}
	if a.focusLabel != nil {
		_ = a.focusLabel.SetText("Run a surface scan to enable file-type focus. System, app, executable, and dependency files are skipped automatically.")
	}
}

func (a *windowsApp) setFileFocusEnabled(enabled bool) {
	for _, item := range a.categoryChecks() {
		if item.check == nil {
			continue
		}
		count := a.surfaceReport.CategoryStats[item.category].Files
		item.check.SetEnabled(enabled && count > 0)
	}
}

func (a *windowsApp) updateFileFocusFromSurfaceReport() {
	for _, item := range a.categoryChecks() {
		if item.check == nil {
			continue
		}
		stats := a.surfaceReport.CategoryStats[item.category]
		_ = item.check.SetText(fmt.Sprintf("%s (%d)", item.label, stats.Files))
		item.check.SetChecked(stats.Files > 0)
	}
	_ = a.focusLabel.SetText(fmt.Sprintf("Surface scan found %d user-created file(s), skipped %d system/app item(s). Select file types, then run duplicate scan.", a.surfaceReport.TotalFiles, a.surfaceReport.SkippedSystemItems))
}

func (a *windowsApp) selectedFileCategories() map[scanner.FileCategory]bool {
	categories := make(map[scanner.FileCategory]bool)
	for _, item := range a.categoryChecks() {
		if item.check != nil && item.check.Enabled() && item.check.Checked() {
			categories[item.category] = true
		}
	}
	return categories
}

type categoryCheck struct {
	category scanner.FileCategory
	label    string
	check    *walk.CheckBox
}

func (a *windowsApp) selectFolder() {
	if !a.operation.canChangeFolder() {
		return
	}

	path, accepted, err := showModernFolderDialog(a.mw, "Select folder to scan", a.operation.folder)
	if err != nil {
		diagnostics.Logf("modern folder picker failed; falling back to legacy picker: error_type=%T", err)
		dlg := walk.FileDialog{
			Title:          "Select folder to scan",
			InitialDirPath: a.operation.folder,
		}
		accepted, err = dlg.ShowBrowseFolder(a.mw)
		if err != nil {
			walk.MsgBox(a.mw, "Folder Selection Failed", displayUntrustedText(err.Error()), walk.MsgBoxOK|walk.MsgBoxIconError)
			return
		}
		path = dlg.FilePath
	}
	if !accepted {
		return
	}
	if err := a.operation.selectFolder(path); err != nil {
		diagnostics.Logf("folder selection rejected: phase=%s error_type=%T", a.operation.phase, err)
		return
	}

	a.surfaceReport = scanner.SurfaceReport{}
	a.model.setRows(nil)
	if a.table != nil {
		_ = a.table.SetSelectedIndexes(nil)
		_ = a.table.SetCurrentIndex(-1)
	}
	a.publishRows()
	a.progress.SetValue(0)
	a.clearFileFocus()
	diagnostics.Logf("folder selected: revision=%d", a.operation.folderRevision)
	_ = a.folderLabel.SetText(displayFilesystemPath(a.operation.folder))
	_ = a.stageLabel.SetText(string(scanner.StageIdle))
	_ = a.filesLabel.SetText("0 files")
	_ = a.elapsedLabel.SetText("0s")
	_ = a.currentLabel.SetText(displayFilesystemPath(a.operation.folder))
	_ = a.statusLabel.SetText("Ready to scan.")
	a.setPreviewMessage("Run a surface scan to inventory eligible user-created files.")
	a.renderFromPhase()
}

func (a *windowsApp) startScan() {
	if a.operation.folder == "" {
		walk.MsgBox(a.mw, "Select Folder", "Choose a folder before scanning.", walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}

	switch a.operation.phase {
	case phaseFolderReady:
		a.startSurfaceScan()
	case phaseSurfaceReady:
		a.startDuplicateScan()
	default:
		diagnostics.Logf("scan request ignored in phase=%s", a.operation.phase)
	}
}

func scanCrashFields(token operationToken) map[string]string {
	return map[string]string{
		"generation":      fmt.Sprint(token.generation),
		"folder_revision": fmt.Sprint(token.folderRevision),
	}
}

func (a *windowsApp) startSurfaceScan() {
	ctx, cancel := context.WithCancel(context.Background())
	token, err := a.operation.beginSurfaceScan(time.Now(), cancel)
	if err != nil {
		cancel()
		diagnostics.Logf("surface scan start rejected: phase=%s error_type=%T", a.operation.phase, err)
		return
	}

	a.surfaceReport = scanner.SurfaceReport{}
	a.model.setRows(nil)
	a.publishRows()
	a.progress.SetValue(0)
	_ = a.stageLabel.SetText(string(scanner.StageSurfaceScan))
	_ = a.filesLabel.SetText("0 files")
	_ = a.elapsedLabel.SetText("0s")
	_ = a.currentLabel.SetText(displayFilesystemPath(token.folder))
	_ = a.statusLabel.SetText("Starting surface scan.")
	a.setPreviewMessage("Scanning user-created files only. System, app, executable, and dependency files are skipped.")
	a.renderFromPhase()

	folder := token.folder
	diagnostics.Logf("surface scan started: generation=%d folder_revision=%d", token.generation, token.folderRevision)

	updates := make(chan scanner.Progress, 512)
	done := make(chan struct{})

	go func() {
		defer func() {
			recovered := recover()
			if err := diagnostics.PanicToError("gui progress updater", recovered, scanCrashFields(token)); err != nil {
				diagnostics.Logf("gui progress updater recovered: generation=%d", token.generation)
			}
		}()
		a.consumeProgress(token, updates)
	}()
	go func() {
		defer func() {
			recovered := recover()
			if err := diagnostics.PanicToError("gui elapsed timer", recovered, scanCrashFields(token)); err != nil {
				diagnostics.Logf("gui elapsed timer recovered: generation=%d", token.generation)
			}
		}()
		a.tickElapsed(token, ctx, done)
	}()
	go func() {
		var report scanner.SurfaceReport
		var err error
		defer func() {
			recovered := recover()
			if crashErr := diagnostics.PanicToError("surface scan workflow", recovered, scanCrashFields(token)); crashErr != nil {
				err = crashErr
			}
			diagnostics.Logf("surface scan ended: generation=%d files=%d failed=%t", token.generation, report.TotalFiles, err != nil)
			close(updates)
			close(done)
			a.synchronizeUI(func() {
				a.surfaceScanFinished(token, report, err)
			})
		}()

		report, err = a.engine.SurfaceScan(ctx, []string{folder}, updates)
	}()
}

func (a *windowsApp) startDuplicateScan() {
	categories := a.selectedFileCategories()
	if len(categories) == 0 {
		walk.MsgBox(a.mw, "Select File Types", "Select at least one file type before finding duplicates.", walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}

	files := append([]scanner.FileRecord(nil), a.surfaceReport.Files...)
	ctx, cancel := context.WithCancel(context.Background())
	token, err := a.operation.beginDuplicateScan(time.Now(), cancel)
	if err != nil {
		cancel()
		diagnostics.Logf("duplicate scan start rejected: phase=%s error_type=%T", a.operation.phase, err)
		return
	}

	a.progress.SetValue(0)
	_ = a.stageLabel.SetText(string(scanner.StageSizeMapping))
	_ = a.filesLabel.SetText("0 files")
	_ = a.elapsedLabel.SetText("0s")
	_ = a.currentLabel.SetText(displayFilesystemPath(token.folder))
	_ = a.statusLabel.SetText("Finding exact duplicates in selected user file types.")
	a.setPreviewMessage("Finding exact duplicates in the selected user-created file categories.")
	a.renderFromPhase()

	diagnostics.Logf("duplicate scan started: generation=%d folder_revision=%d categories=%s", token.generation, token.folderRevision, categorySelectionSummary(categories))

	updates := make(chan scanner.Progress, 512)
	done := make(chan struct{})

	go func() {
		defer func() {
			recovered := recover()
			if err := diagnostics.PanicToError("gui progress updater", recovered, scanCrashFields(token)); err != nil {
				diagnostics.Logf("gui progress updater recovered: generation=%d", token.generation)
			}
		}()
		a.consumeProgress(token, updates)
	}()
	go func() {
		defer func() {
			recovered := recover()
			if err := diagnostics.PanicToError("gui elapsed timer", recovered, scanCrashFields(token)); err != nil {
				diagnostics.Logf("gui elapsed timer recovered: generation=%d", token.generation)
			}
		}()
		a.tickElapsed(token, ctx, done)
	}()
	go func() {
		var groups []scanner.DuplicateGroup
		var err error
		defer func() {
			recovered := recover()
			if crashErr := diagnostics.PanicToError("duplicate scan workflow", recovered, scanCrashFields(token)); crashErr != nil {
				err = crashErr
			}
			diagnostics.Logf("duplicate scan ended: generation=%d groups=%d failed=%t", token.generation, len(groups), err != nil)
			close(updates)
			close(done)
			a.synchronizeUI(func() {
				a.scanFinished(token, groups, err)
			})
		}()

		groups, err = a.engine.ScanFiles(ctx, files, scanner.ScanOptions{
			Categories:    categories,
			UserFilesOnly: true,
		}, updates)
	}()
}

func (a *windowsApp) consumeProgress(token operationToken, updates <-chan scanner.Progress) {
	for progress := range updates {
		p := progress
		a.synchronizeUI(func() {
			if !a.operation.accepts(token) {
				return
			}
			_ = a.stageLabel.SetText(string(p.Stage))
			if p.CurrentPath != "" {
				_ = a.currentLabel.SetText(displayFilesystemPath(p.CurrentPath))
			}
			if p.FilesTotal > 0 {
				_ = a.filesLabel.SetText(fmt.Sprintf("%d / %d files", p.FilesProcessed, p.FilesTotal))
				a.progress.SetValue(int((p.FilesProcessed * 1000) / p.FilesTotal))
			} else if p.FilesProcessed > 0 {
				_ = a.filesLabel.SetText(fmt.Sprintf("%d files", p.FilesProcessed))
			}
			if p.Stage == scanner.StageDone {
				a.progress.SetValue(1000)
			}
			if p.Message != "" {
				status := p.Message
				if p.ErrorsIgnored > 0 {
					status = fmt.Sprintf("%s Ignored %d locked or unreadable item(s).", status, p.ErrorsIgnored)
				}
				if p.SkippedSystemItems > 0 {
					status = fmt.Sprintf("%s Skipped %d system/app item(s).", status, p.SkippedSystemItems)
				}
				_ = a.statusLabel.SetText(status)
			}
		})
	}
}

func (a *windowsApp) tickElapsed(token operationToken, ctx context.Context, done <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			elapsed := formatDuration(time.Since(token.startedAt))
			a.synchronizeUI(func() {
				if !a.operation.accepts(token) {
					return
				}
				_ = a.elapsedLabel.SetText(elapsed)
			})
		}
	}
}

func (a *windowsApp) surfaceScanFinished(token operationToken, report scanner.SurfaceReport, err error) {
	cancelled := a.operation.phase == phaseSurfaceCancelling || errors.Is(err, context.Canceled)
	if !a.operation.completeSurfaceScan(token, err == nil) {
		diagnostics.Logf("stale surface completion ignored: generation=%d folder_revision=%d", token.generation, token.folderRevision)
		return
	}

	if cancelled {
		diagnostics.Logf("surface scan canceled: generation=%d folder_revision=%d", token.generation, token.folderRevision)
		_ = a.statusLabel.SetText("Surface scan canceled.")
		a.renderFromPhase()
		return
	}
	if err != nil {
		diagnostics.Logf("surface scan failed: generation=%d folder_revision=%d error_type=%T", token.generation, token.folderRevision, err)
		_ = a.statusLabel.SetText("Surface scan failed.")
		a.renderFromPhase()
		walk.MsgBox(a.mw, "Surface Scan Failed", displayUntrustedText(err.Error()), walk.MsgBoxOK|walk.MsgBoxIconError)
		return
	}

	a.surfaceReport = report
	a.updateFileFocusFromSurfaceReport()
	a.model.setSurfaceFiles(report.Files, "Scanned")
	a.publishRows()
	a.progress.SetValue(1000)
	_ = a.stageLabel.SetText(string(scanner.StageSurfaceScan))
	_ = a.filesLabel.SetText(fmt.Sprintf("%d user files", report.TotalFiles))
	_ = a.elapsedLabel.SetText(formatDuration(time.Since(token.startedAt)))
	_ = a.statusLabel.SetText(fmt.Sprintf("Surface scan complete. Found %d user-created file(s), skipped %d system/app item(s).", report.TotalFiles, report.SkippedSystemItems))
	a.setPreviewMessage("Surface scan complete. The table lists all eligible user-created files. Select file types, then run duplicate scan.")
	a.renderFromPhase()
	diagnostics.Logf("surface results published: generation=%d folder_revision=%d files=%d skipped=%d", token.generation, token.folderRevision, report.TotalFiles, report.SkippedSystemItems)
}

func (a *windowsApp) scanFinished(token operationToken, groups []scanner.DuplicateGroup, err error) {
	cancelled := a.operation.phase == phaseDuplicateCancelling || errors.Is(err, context.Canceled)
	if !a.operation.completeDuplicateScan(token, err == nil) {
		diagnostics.Logf("stale duplicate completion ignored: generation=%d folder_revision=%d", token.generation, token.folderRevision)
		return
	}

	if cancelled {
		diagnostics.Logf("duplicate scan canceled: generation=%d folder_revision=%d", token.generation, token.folderRevision)
		_ = a.statusLabel.SetText("Duplicate scan canceled. Surface results remain available.")
		a.renderFromPhase()
		return
	}
	if err != nil {
		diagnostics.Logf("duplicate scan failed: generation=%d folder_revision=%d error_type=%T", token.generation, token.folderRevision, err)
		_ = a.statusLabel.SetText("Duplicate scan failed. Surface results remain available.")
		a.renderFromPhase()
		walk.MsgBox(a.mw, "Scan Failed", displayUntrustedText(err.Error()), walk.MsgBoxOK|walk.MsgBoxIconError)
		return
	}

	if len(groups) == 0 {
		a.model.setSurfaceFiles(a.surfaceFilesForSelectedCategories(), "No duplicate")
	} else {
		a.model.setGroups(groups)
	}
	diagnostics.Logf("scan results published: generation=%d folder_revision=%d groups=%d rows=%d", token.generation, token.folderRevision, len(groups), len(a.model.rows))
	a.publishRows()
	a.progress.SetValue(1000)
	_ = a.stageLabel.SetText(string(scanner.StageDone))
	_ = a.elapsedLabel.SetText(formatDuration(time.Since(token.startedAt)))
	if len(groups) == 0 {
		_ = a.statusLabel.SetText(fmt.Sprintf("No exact duplicates found. Showing %d scanned file(s) from the selected focus categories.", len(a.model.rows)))
		a.setPreviewMessage("No exact duplicates were found. The table remains populated for review, preview, and opening file locations.")
	} else {
		_ = a.statusLabel.SetText(fmt.Sprintf("Found %d exact duplicate group(s), containing %d duplicate file record(s).", len(groups), len(a.model.rows)))
		a.setPreviewMessage("Select a row to inspect file details, exact hash, and a visual preview for supported file types.")
	}

	a.renderFromPhase()
}

func (a *windowsApp) surfaceFilesForSelectedCategories() []scanner.FileRecord {
	categories := a.selectedFileCategories()
	if len(categories) == 0 {
		return nil
	}
	files := make([]scanner.FileRecord, 0, len(a.surfaceReport.Files))
	for _, file := range a.surfaceReport.Files {
		category := file.Category
		if category == "" {
			category = scanner.CategoryForPath(file.Path)
			file.Category = category
		}
		if categories[category] {
			files = append(files, file)
		}
	}
	return files
}

func (a *windowsApp) cancelScan() {
	if a.operation.requestScanCancellation() {
		_ = a.statusLabel.SetText("Cancelling scan. Waiting for active file work to stop.")
		a.renderFromPhase()
	}
}

func (a *windowsApp) clearResults() {
	phase := a.operation.phase
	if phase != phaseSurfaceReady && phase != phaseResultsReady {
		return
	}
	if phase == phaseResultsReady {
		if err := a.operation.clearResults(); err != nil {
			diagnostics.Logf("clear results rejected: phase=%s error_type=%T", a.operation.phase, err)
			return
		}
	}

	a.model.setRows(nil)
	if a.table != nil {
		_ = a.table.SetSelectedIndexes(nil)
		_ = a.table.SetCurrentIndex(-1)
	}
	a.publishRows()
	a.progress.SetValue(0)
	_ = a.stageLabel.SetText(string(scanner.StageSurfaceScan))
	_ = a.statusLabel.SetText("Results cleared. Adjust file focus or run duplicate scan again.")
	_ = a.filesLabel.SetText("0 files")
	_ = a.currentLabel.SetText(displayFilesystemPath(a.operation.folder))
	a.setPreviewMessage("Results cleared. Start a scan to find exact duplicates.")
	a.renderFromPhase()
}

func (a *windowsApp) resetAll() {
	if err := a.operation.reset(); err != nil {
		diagnostics.Logf("reset rejected: phase=%s error_type=%T", a.operation.phase, err)
		return
	}

	a.surfaceReport = scanner.SurfaceReport{}
	a.model.setRows(nil)
	if a.table != nil {
		_ = a.table.SetSelectedIndexes(nil)
		_ = a.table.SetCurrentIndex(-1)
	}
	a.publishRows()
	a.progress.SetValue(0)
	_ = a.folderLabel.SetText("No folder selected")
	_ = a.stageLabel.SetText(string(scanner.StageIdle))
	_ = a.filesLabel.SetText("0 files")
	_ = a.elapsedLabel.SetText("0s")
	_ = a.currentLabel.SetText("-")
	_ = a.statusLabel.SetText("Select a folder to start.")
	a.clearFileFocus()
	a.setPreviewMessage("Select a duplicate row to show an Explorer-style visual preview and verification details.")
	a.renderFromPhase()
}

func (a *windowsApp) openLogs() {
	dir := diagnostics.LogDir()
	if dir == "" {
		walk.MsgBox(a.mw, "Logs", "Diagnostics logging is not initialized for this run.", walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		walk.MsgBox(a.mw, "Logs", displayUntrustedText(err.Error()), walk.MsgBoxOK|walk.MsgBoxIconError)
		return
	}
	if err := startExplorer(dir, false); err != nil {
		walk.MsgBox(a.mw, "Logs", "Could not open the diagnostics folder.\r\n\r\nFolder: "+displayFilesystemPath(dir), walk.MsgBoxOK|walk.MsgBoxIconError)
	}
}

func (a *windowsApp) showAbout() {
	message := buildinfo.Summary() +
		"\r\n\r\nTwinTidy finds and reviews byte-for-byte duplicate files. Cleanup is disabled in this pre-release build until the Windows Recycle Bin operation can remain bound to the verified file identity." +
		"\r\n\r\nLocal-only processing. No telemetry. No file deletion." +
		"\r\n\r\nProduction targets: Windows x64 and ARM64."
	walk.MsgBox(a.mw, "About TwinTidy", message, walk.MsgBoxOK|walk.MsgBoxIconInformation)
}

func (a *windowsApp) selectAllExceptNewest() {
	a.model.selectAllExcept(true)
	a.publishRows()
	a.updateDeleteActionState()
	_ = a.statusLabel.SetText(fmt.Sprintf("Selected %d duplicate file(s), keeping the newest file in each group.", a.model.selectedCount()))
}

func (a *windowsApp) selectAllExceptOldest() {
	a.model.selectAllExcept(false)
	a.publishRows()
	a.updateDeleteActionState()
	_ = a.statusLabel.SetText(fmt.Sprintf("Selected %d duplicate file(s), keeping the oldest file in each group.", a.model.selectedCount()))
}

func (a *windowsApp) clearSelection() {
	a.model.clearSelection()
	if a.table != nil {
		_ = a.table.SetSelectedIndexes(nil)
		_ = a.table.SetCurrentIndex(-1)
	}
	a.publishRows()
	a.updateDeleteActionState()
	_ = a.statusLabel.SetText("Selection cleared.")
}

func (a *windowsApp) showSelectedInExplorer() {
	index := a.table.CurrentIndex()
	if index < 0 || index >= len(a.model.rows) {
		walk.MsgBox(a.mw, "Select A File", "Select a row first.", walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}
	path := a.model.rows[index].File.Path
	if err := startExplorer(path, true); err != nil {
		walk.MsgBox(a.mw, "Show In Explorer", "Windows Explorer could not show the selected file.\r\n\r\nFile: "+displayFilesystemPath(path), walk.MsgBoxOK|walk.MsgBoxIconError)
	}
}

func startExplorer(path string, selectFile bool) error {
	explorer, err := trustedExplorerPath()
	if err != nil {
		return err
	}
	args := []string{path}
	if selectFile {
		args[0] = "/select," + path
	}
	return exec.Command(explorer, args...).Start()
}

const (
	maxRecycleConfirmationEntries = 40
	maxRecycleConfirmationChars   = 8000
)

var (
	errRecycleWouldRemoveAllCopies = errors.New("recycle selection would remove every physical copy in a duplicate group")
	errRecycleConfirmationTooLarge = errors.New("recycle confirmation is too large for the native confirmation dialog")
)

type plannedRecycleGroup struct {
	groupNumber int
	request     scanner.RecycleRequest
}

type recyclePlan struct {
	groups        []plannedRecycleGroup
	selectedCount int
}

type recycleGroupResult struct {
	groupNumber int
	result      scanner.RecycleResult
}

type recycleBatchResult struct {
	groups        []recycleGroupResult
	internalError string
}

type recycleOutcome struct {
	recycled        int
	changed         int
	protected       int
	cancelled       int
	failed          int
	requestErrors   []string
	internalError   string
	nonRecycledRows []scanner.RecycleItemResult
}

type physicalCopyKey struct {
	identity scanner.FileIdentity
	path     string
}

func recyclePhysicalCopyKey(file scanner.FileRecord) physicalCopyKey {
	if file.Identity != (scanner.FileIdentity{}) {
		return physicalCopyKey{identity: file.Identity}
	}
	return physicalCopyKey{path: strings.ToLower(filepath.Clean(file.Path))}
}

func buildRecyclePlan(rows []duplicateRow) (recyclePlan, error) {
	type groupAccumulator struct {
		groupNumber int
		hash        string
		size        int64
		files       []scanner.FileRecord
		selected    []scanner.FileRecord
	}

	groups := make(map[string]*groupAccumulator)
	order := make([]string, 0)
	for _, row := range rows {
		if !row.Duplicate {
			continue
		}
		key := row.Key
		if key == "" {
			key = fmt.Sprintf("%d:%s", row.File.Size, row.Hash)
		}
		group := groups[key]
		if group == nil {
			group = &groupAccumulator{groupNumber: row.Group, hash: row.Hash, size: row.File.Size}
			groups[key] = group
			order = append(order, key)
		}
		if group.hash != row.Hash || group.size != row.File.Size {
			return recyclePlan{}, fmt.Errorf("duplicate group %d contains inconsistent scan data", group.groupNumber)
		}
		group.files = append(group.files, row.File)
		if row.Selected {
			group.selected = append(group.selected, row.File)
		}
	}

	plan := recyclePlan{groups: make([]plannedRecycleGroup, 0, len(order))}
	for _, key := range order {
		group := groups[key]
		if len(group.selected) == 0 {
			continue
		}

		allCopies := make(map[physicalCopyKey]struct{}, len(group.files))
		selectedCopies := make(map[physicalCopyKey]struct{}, len(group.selected))
		for _, file := range group.files {
			allCopies[recyclePhysicalCopyKey(file)] = struct{}{}
		}
		for _, file := range group.selected {
			selectedCopies[recyclePhysicalCopyKey(file)] = struct{}{}
		}
		if len(allCopies) == 0 || len(selectedCopies) >= len(allCopies) {
			return recyclePlan{}, fmt.Errorf("group %d: %w", group.groupNumber, errRecycleWouldRemoveAllCopies)
		}

		files := append([]scanner.FileRecord(nil), group.files...)
		selected := append([]scanner.FileRecord(nil), group.selected...)
		plan.groups = append(plan.groups, plannedRecycleGroup{
			groupNumber: group.groupNumber,
			request: scanner.RecycleRequest{
				Group: scanner.DuplicateGroup{
					Size:  group.size,
					Hash:  group.hash,
					Files: files,
				},
				Selected: selected,
			},
		})
		plan.selectedCount += len(selected)
	}
	if plan.selectedCount == 0 {
		return recyclePlan{}, errors.New("no checked duplicate files were selected")
	}
	return plan, nil
}

func recycleConfirmationMessage(plan recyclePlan) (string, error) {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Move exactly %d checked duplicate file(s) to the Windows Recycle Bin?\r\n\r\n", plan.selectedCount))
	builder.WriteString("TwinTidy preserves at least one physical copy per group and revalidates file identity, content, and keeper availability immediately before each recycle operation. Changed, protected, cancelled, or failed files remain in place.\r\n")
	builder.WriteString("TwinTidy uses the Windows Recycle Bin only and never permanently deletes files.\r\n\r\n")

	entryCount := 0
	for _, planned := range plan.groups {
		builder.WriteString(fmt.Sprintf("Group %d\r\n", planned.groupNumber))
		selectedPaths := make(map[string]struct{}, len(planned.request.Selected))
		for _, selected := range planned.request.Selected {
			selectedPaths[strings.ToLower(filepath.Clean(selected.Path))] = struct{}{}
			builder.WriteString("  RECYCLE: ")
			builder.WriteString(displayFilesystemPath(selected.Path))
			builder.WriteString("\r\n")
			entryCount++
		}
		for _, file := range planned.request.Group.Files {
			if _, selected := selectedPaths[strings.ToLower(filepath.Clean(file.Path))]; selected {
				continue
			}
			builder.WriteString("  KEEP:    ")
			builder.WriteString(displayFilesystemPath(file.Path))
			builder.WriteString("\r\n")
			entryCount++
		}
		builder.WriteString("\r\n")
	}

	if entryCount > maxRecycleConfirmationEntries || builder.Len() > maxRecycleConfirmationChars {
		return "", errRecycleConfirmationTooLarge
	}
	return builder.String(), nil
}

func (a *windowsApp) confirmDeleteSelected() {
	if !scanner.RecycleSupported() {
		walk.MsgBox(a.mw, "Recycle Unavailable", "No files were changed. TwinTidy disables cleanup until Windows Recycle Bin operations can stay bound to the verified file identity throughout the native operation.", walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}
	plan, err := buildRecyclePlan(a.model.rows)
	if err != nil {
		if errors.Is(err, errRecycleWouldRemoveAllCopies) {
			walk.MsgBox(a.mw, "Keep One Copy", "TwinTidy will not recycle every physical copy in a duplicate group. Uncheck at least one verified keeper in each group.", walk.MsgBoxOK|walk.MsgBoxIconWarning)
			return
		}
		walk.MsgBox(a.mw, "Cannot Recycle Selection", displayUntrustedText(err.Error()), walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}

	message, err := recycleConfirmationMessage(plan)
	if err != nil {
		walk.MsgBox(a.mw, "Review A Smaller Batch", fmt.Sprintf("This selection contains %d checked target(s) and too many target/keeper paths for the native confirmation dialog. No files were changed.\r\n\r\nSelect a smaller batch so TwinTidy can show every checked target and retained keeper before approval.", plan.selectedCount), walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}
	result := walk.MsgBox(a.mw, "Recycle Checked Duplicates", message, walk.MsgBoxYesNo|walk.MsgBoxIconWarning)
	if result != win.IDYES {
		return
	}

	token, err := a.operation.beginDelete(time.Now())
	if err != nil {
		diagnostics.Logf("recycle start rejected: phase=%s error_type=%T", a.operation.phase, err)
		return
	}
	a.setPreviewMessage("Revalidating checked duplicate files before moving them to the Windows Recycle Bin.")
	_ = a.statusLabel.SetText("Revalidating and recycling checked duplicate files. This operation cannot be cancelled midway.")
	a.renderFromPhase()
	diagnostics.Logf("recycle started: generation=%d groups=%d selected=%d", token.generation, len(plan.groups), plan.selectedCount)

	go func(plan recyclePlan, token operationToken) {
		batch := recycleBatchResult{groups: make([]recycleGroupResult, 0, len(plan.groups))}
		defer func() {
			fields := map[string]string{
				"generation":     fmt.Sprint(token.generation),
				"group_count":    fmt.Sprint(len(plan.groups)),
				"selected_count": fmt.Sprint(plan.selectedCount),
			}
			recovered := recover()
			if crashErr := diagnostics.PanicToError("recycle workflow", recovered, fields); crashErr != nil {
				batch.internalError = crashErr.Error()
			}
			outcome := summarizeRecycleBatch(batch)
			diagnostics.Logf("recycle ended: generation=%d recycled=%d changed=%d protected=%d cancelled=%d failed=%d request_errors=%d internal_error=%t", token.generation, outcome.recycled, outcome.changed, outcome.protected, outcome.cancelled, outcome.failed, len(outcome.requestErrors), outcome.internalError != "")
			a.synchronizeUI(func() {
				a.applyRecycleBatch(token, batch)
			})
		}()

		ctx := context.Background()
		for _, planned := range plan.groups {
			batch.groups = append(batch.groups, recycleGroupResult{
				groupNumber: planned.groupNumber,
				result:      scanner.RecycleExactDuplicates(ctx, planned.request),
			})
		}
	}(plan, token)
}

func summarizeRecycleBatch(batch recycleBatchResult) recycleOutcome {
	outcome := recycleOutcome{internalError: batch.internalError}
	for _, group := range batch.groups {
		if group.result.RequestError != "" {
			outcome.requestErrors = append(outcome.requestErrors, fmt.Sprintf("Group %d: %s", group.groupNumber, group.result.RequestError))
		}
		for _, item := range group.result.Items {
			switch item.Status {
			case scanner.RecycleStatusRecycled:
				outcome.recycled++
			case scanner.RecycleStatusSkippedChanged:
				outcome.changed++
				outcome.nonRecycledRows = append(outcome.nonRecycledRows, item)
			case scanner.RecycleStatusSkippedProtected:
				outcome.protected++
				outcome.nonRecycledRows = append(outcome.nonRecycledRows, item)
			case scanner.RecycleStatusCancelled:
				outcome.cancelled++
				outcome.nonRecycledRows = append(outcome.nonRecycledRows, item)
			default:
				outcome.failed++
				outcome.nonRecycledRows = append(outcome.nonRecycledRows, item)
			}
		}
	}
	return outcome
}

func retainedRecycleStatus(status scanner.RecycleStatus) string {
	switch status {
	case scanner.RecycleStatusSkippedChanged:
		return "Changed - kept"
	case scanner.RecycleStatusSkippedProtected:
		return "Protected - kept"
	case scanner.RecycleStatusCancelled:
		return "Cancelled - kept"
	default:
		return "Recycle failed - kept"
	}
}

func applyRecycleBatchToModel(model *duplicateTableModel, batch recycleBatchResult) recycleOutcome {
	outcome := summarizeRecycleBatch(batch)
	itemsByPath := make(map[string]scanner.RecycleItemResult)
	deleted := make(map[string]struct{})
	for _, group := range batch.groups {
		for _, item := range group.result.Items {
			pathKey := strings.ToLower(filepath.Clean(item.Path))
			itemsByPath[pathKey] = item
			if item.Status == scanner.RecycleStatusRecycled {
				deleted[item.Path] = struct{}{}
			}
		}
	}
	for index := range model.rows {
		model.rows[index].Selected = false
		item, exists := itemsByPath[strings.ToLower(filepath.Clean(model.rows[index].File.Path))]
		if exists && item.Status != scanner.RecycleStatusRecycled {
			model.rows[index].Status = retainedRecycleStatus(item.Status)
		}
	}
	model.removeDeleted(deleted)
	return outcome
}

func (a *windowsApp) applyRecycleBatch(token operationToken, batch recycleBatchResult) {
	if !a.operation.accepts(token) {
		diagnostics.Logf("stale recycle completion ignored: generation=%d folder_revision=%d", token.generation, token.folderRevision)
		return
	}

	outcome := applyRecycleBatchToModel(a.model, batch)
	accepted, shouldClose := a.operation.completeDelete(token)
	if !accepted {
		return
	}
	if a.table != nil {
		_ = a.table.SetSelectedIndexes(nil)
		_ = a.table.SetCurrentIndex(-1)
	}
	a.publishRows()
	a.progress.SetValue(1000)
	a.renderFromPhase()

	status := recycleOutcomeSummary(outcome)
	_ = a.statusLabel.SetText(status)
	if len(a.model.rows) == 0 {
		a.setPreviewMessage("No duplicate groups remain after the Recycle Bin operation.")
	}
	if shouldClose {
		a.mw.Close()
		return
	}
	if recycleOutcomeHasIssues(outcome) {
		walk.MsgBox(a.mw, "Recycle Results", recycleIssueSummary(outcome), walk.MsgBoxOK|walk.MsgBoxIconWarning)
	}
}

func (a *windowsApp) updatePreviewFromSelection() {
	a.updateDeleteActionState()
	if a.operation.phase != phaseSurfaceReady && a.operation.phase != phaseResultsReady {
		a.invalidatePreview()
		a.openButton.SetEnabled(false)
		a.previewSafetyButton.SetEnabled(false)
		return
	}

	selectedRows := a.selectedRows()
	if len(selectedRows) > 1 {
		a.openButton.SetEnabled(true)
		a.previewSafetyButton.SetEnabled(false)
		a.requestPreparedPreview(previewModeComparison, selectedRows)
		return
	}

	index := a.table.CurrentIndex()
	if index < 0 || index >= len(a.model.rows) {
		a.setPreviewMessage("Select a duplicate row to show an Explorer-style visual preview and verification details.")
		a.openButton.SetEnabled(false)
		a.previewSafetyButton.SetEnabled(false)
		return
	}
	a.openButton.SetEnabled(true)
	row := a.model.rows[index]
	a.previewSafetyButton.SetEnabled(true)
	a.requestPreparedPreview(previewModeSingle, []duplicateRow{row})
}

func (a *windowsApp) selectedRows() []duplicateRow {
	if a.table == nil {
		return nil
	}

	indexes := append([]int(nil), a.table.SelectedIndexes()...)
	sort.Ints(indexes)

	rows := make([]duplicateRow, 0, len(indexes))
	seen := make(map[int]struct{}, len(indexes))
	for _, index := range indexes {
		if index < 0 || index >= len(a.model.rows) {
			continue
		}
		if _, ok := seen[index]; ok {
			continue
		}
		seen[index] = struct{}{}
		rows = append(rows, a.model.rows[index])
	}
	return rows
}

func (a *windowsApp) checkedDeletePaths() []string {
	return a.model.selectedPaths()
}

func (a *windowsApp) updateDeleteActionState() {
	if a.deleteButton == nil {
		return
	}
	controls := controlsForOperation(&a.operation, len(a.model.rows) > 0, rowsContainDuplicates(a.model.rows), a.model.selectedCount())
	a.deleteButton.SetEnabled(controls.deleteSelected && scanner.RecycleSupported())
}

func (a *windowsApp) publishRows() {
	a.invalidatePreview()
	a.model.PublishRowsReset()
	_ = a.table.Invalidate()
}

func (a *windowsApp) setPreviewMessage(message string) {
	a.invalidatePreview()
	a.setPreviewImage(nil)
	_ = a.previewText.SetText(message)
}

func (a *windowsApp) setPreviewImage(image walk.Image) {
	old := a.currentPreviewImage
	a.currentPreviewImage = image
	a.webPreview.SetVisible(false)
	a.previewImage.SetVisible(true)
	_ = a.previewImage.SetImage(image)
	if old != nil {
		old.Dispose()
	}
}

func (a *windowsApp) showPreviewSafety() {
	walk.MsgBox(
		a.mw,
		"Preview Safety",
		"TwinTidy does not load scanned PDF, Office, or media files into its embedded browser control. It shows Windows Shell thumbnails, bounded plain-text previews, and metadata only.\r\n\r\nUse Show In Explorer, then open a file with an application you trust if you need to inspect its full contents.",
		walk.MsgBoxOK|walk.MsgBoxIconInformation,
	)
}

func (m *duplicateTableModel) RowCount() int {
	return len(m.rows)
}

func (m *duplicateTableModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	item := m.rows[row]
	switch col {
	case 0:
		return item.Number
	case 1:
		return item.Status
	case 2:
		if item.Group == 0 {
			return "-"
		}
		return item.Group
	case 3:
		return displayFilesystemName(item.File.Path)
	case 4:
		return formatBytes(item.File.Size)
	case 5:
		return displayUntrustedText(fileType(item.File.Path))
	case 6:
		return formatTime(item.File.ModifiedAt)
	case 7:
		return formatTime(item.File.CreatedAt)
	case 8:
		if item.Hash == "" {
			return "-"
		}
		return shortHash(item.Hash)
	case 9:
		return displayFilesystemPath(item.File.Path)
	default:
		return ""
	}
}

func (m *duplicateTableModel) Checked(index int) bool {
	if index < 0 || index >= len(m.rows) {
		return false
	}
	if !m.rows[index].Duplicate {
		return false
	}
	return m.rows[index].Selected
}

func (m *duplicateTableModel) SetChecked(index int, checked bool) error {
	if index < 0 || index >= len(m.rows) {
		return nil
	}
	previous := m.rows[index].Selected
	if !m.rows[index].Duplicate {
		m.rows[index].Selected = false
		m.PublishRowChanged(index)
		if previous != m.rows[index].Selected {
			m.notifySelectionChanged()
		}
		return nil
	}
	m.rows[index].Selected = checked
	m.PublishRowChanged(index)
	if previous != checked {
		m.notifySelectionChanged()
	}
	return nil
}

func (m *duplicateTableModel) setGroups(groups []scanner.DuplicateGroup) {
	rows := make([]duplicateRow, 0)
	number := 1
	for groupIndex, group := range groups {
		key := fmt.Sprintf("%d:%s", group.Size, group.Hash)
		for _, file := range group.Files {
			rows = append(rows, duplicateRow{
				Number:    number,
				Group:     groupIndex + 1,
				Key:       key,
				Hash:      group.Hash,
				Status:    "Duplicate",
				Duplicate: true,
				File:      file,
			})
			number++
		}
	}
	m.setRows(rows)
}

func (m *duplicateTableModel) setSurfaceFiles(files []scanner.FileRecord, status string) {
	rows := make([]duplicateRow, 0, len(files))
	sorted := append([]scanner.FileRecord(nil), files...)
	sort.Slice(sorted, func(i, j int) bool {
		leftCategory := scanner.CategoryLabel(sorted[i].Category)
		rightCategory := scanner.CategoryLabel(sorted[j].Category)
		if leftCategory != rightCategory {
			return leftCategory < rightCategory
		}
		return sorted[i].Path < sorted[j].Path
	})
	for i, file := range sorted {
		rows = append(rows, duplicateRow{
			Number:    i + 1,
			Key:       "surface:" + file.Path,
			Status:    status,
			Duplicate: false,
			File:      file,
		})
	}
	m.setRows(rows)
}

func (m *duplicateTableModel) setRows(rows []duplicateRow) {
	m.rows = rows
}

func (m *duplicateTableModel) selectedCount() int {
	count := 0
	for _, row := range m.rows {
		if row.Duplicate && row.Selected {
			count++
		}
	}
	return count
}

func (m *duplicateTableModel) selectedPaths() []string {
	paths := make([]string, 0)
	for _, row := range m.rows {
		if row.Duplicate && row.Selected {
			paths = append(paths, row.File.Path)
		}
	}
	sort.Strings(paths)
	return paths
}

func (m *duplicateTableModel) clearSelection() {
	for i := range m.rows {
		m.rows[i].Selected = false
	}
}

func (m *duplicateTableModel) notifySelectionChanged() {
	if m.onSelectionChanged != nil {
		m.onSelectionChanged()
	}
}

func (m *duplicateTableModel) selectAllExcept(keepNewest bool) {
	keepers := make(map[string]int)
	for i, row := range m.rows {
		if !row.Duplicate {
			continue
		}
		current, ok := keepers[row.Key]
		if !ok {
			keepers[row.Key] = i
			continue
		}
		if keepNewest {
			if row.File.ModifiedAt.After(m.rows[current].File.ModifiedAt) {
				keepers[row.Key] = i
			}
		} else if row.File.ModifiedAt.Before(m.rows[current].File.ModifiedAt) {
			keepers[row.Key] = i
		}
	}

	for i := range m.rows {
		m.rows[i].Selected = m.rows[i].Duplicate && keepers[m.rows[i].Key] != i
	}
}

func (m *duplicateTableModel) removeDeleted(deleted map[string]struct{}) {
	filtered := make([]duplicateRow, 0, len(m.rows))
	counts := make(map[string]int)
	for _, row := range m.rows {
		if _, ok := deleted[row.File.Path]; ok {
			continue
		}
		filtered = append(filtered, row)
		counts[row.Key]++
	}

	renumbered := make([]duplicateRow, 0, len(filtered))
	groupNumbers := make(map[string]int)
	nextGroup := 1
	for _, row := range filtered {
		if counts[row.Key] < 2 {
			continue
		}
		group, ok := groupNumbers[row.Key]
		if !ok {
			group = nextGroup
			groupNumbers[row.Key] = group
			nextGroup++
		}
		row.Group = group
		row.Number = len(renumbered) + 1
		row.Selected = false
		renumbered = append(renumbered, row)
	}
	m.rows = renumbered
}

func previewDetailsForRow(row duplicateRow) string {
	var builder strings.Builder
	builder.WriteString("Status: ")
	if row.Status == "" {
		builder.WriteString("Scanned")
	} else {
		builder.WriteString(row.Status)
	}
	if row.Duplicate {
		builder.WriteString("\r\nExact duplicate group: ")
		builder.WriteString(fmt.Sprint(row.Group))
		builder.WriteString("\r\nFull SHA-256: ")
		builder.WriteString(row.Hash)
	}
	builder.WriteString("\r\nName: ")
	builder.WriteString(displayFilesystemName(row.File.Path))
	builder.WriteString("\r\nCategory: ")
	builder.WriteString(scanner.CategoryLabel(row.File.Category))
	builder.WriteString("\r\nType: ")
	builder.WriteString(displayUntrustedText(fileType(row.File.Path)))
	builder.WriteString("\r\nSize: ")
	builder.WriteString(formatBytes(row.File.Size))
	builder.WriteString("\r\nCreated: ")
	builder.WriteString(formatTime(row.File.CreatedAt))
	builder.WriteString("\r\nModified: ")
	builder.WriteString(formatTime(row.File.ModifiedAt))
	builder.WriteString("\r\nPath: ")
	builder.WriteString(displayFilesystemPath(row.File.Path))
	return builder.String()
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04")
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	return d.Truncate(time.Second).String()
}

func categorySelectionSummary(categories map[scanner.FileCategory]bool) string {
	labels := make([]string, 0, len(categories))
	for _, definition := range scanner.FileCategoryDefinitions() {
		if categories[definition.Category] {
			labels = append(labels, definition.Label)
		}
	}
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, ", ")
}

func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func fileType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return "file"
	}
	return strings.TrimPrefix(ext, ".")
}

func isImageExt(ext string) bool {
	switch ext {
	case ".bmp", ".dib", ".gif", ".jpg", ".jpeg", ".jpe", ".png", ".tif", ".tiff", ".ico", ".emf", ".wmf", ".exif":
		return true
	default:
		return false
	}
}

func isOfficeExt(ext string) bool {
	switch ext {
	case ".doc", ".docx", ".docm", ".rtf",
		".xls", ".xlsx", ".xlsm", ".csv",
		".ppt", ".pptx", ".pptm":
		return true
	default:
		return false
	}
}

func isAudioExt(ext string) bool {
	switch ext {
	case ".mp3", ".m4a", ".aac", ".wav", ".wma", ".flac", ".ogg":
		return true
	default:
		return false
	}
}

func isVideoExt(ext string) bool {
	switch ext {
	case ".mp4", ".m4v", ".mov", ".avi", ".mkv", ".wmv", ".webm", ".mpeg", ".mpg":
		return true
	default:
		return false
	}
}

func isTextExt(ext string) bool {
	switch ext {
	case ".txt", ".md", ".csv", ".tsv", ".json", ".xml", ".html", ".css", ".js", ".go", ".py", ".log", ".ini", ".yaml", ".yml", ".sql", ".ps1":
		return true
	default:
		return false
	}
}

func readVerifiedTextPreview(record scanner.FileRecord) (string, error) {
	file, err := scanner.OpenVerifiedRecordForRead(record)
	if err != nil {
		return "", err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, previewLimitBytes+1))
	if err != nil {
		return "", err
	}
	text := string(data)
	if int64(len(data)) > previewLimitBytes {
		text = string(data[:int(previewLimitBytes)]) + "\r\n\r\n[Preview truncated]"
	}
	return text, nil
}

func (a *windowsApp) guardPreviewNavigation(event *walk.WebViewNavigatingEventData) {
	if !previewNavigationAllowed(event.Url(), a.allowedPreviewURL) {
		event.SetCanceled(true)
	}
}

type previewFallback struct {
	Badge      string
	BadgeClass string
	Title      string
	Text       string
}

func fallbackForRow(row duplicateRow) previewFallback {
	ext := strings.ToLower(filepath.Ext(row.File.Path))
	label := strings.ToUpper(strings.TrimPrefix(ext, "."))
	if label == "" {
		label = "FILE"
	}
	label = displayUntrustedText(label)

	switch {
	case ext == ".pdf":
		return previewFallback{Badge: "PDF", BadgeClass: "pdf", Title: "PDF document", Text: "Shell thumbnail unavailable. TwinTidy does not parse PDF contents in-process; use Show In Explorer to inspect the file with a trusted application."}
	case isOfficeExt(ext):
		return previewFallback{Badge: label, BadgeClass: "office", Title: "Office document", Text: "Shell thumbnail unavailable. Use Show In Explorer to inspect the file with a trusted application."}
	case isAudioExt(ext):
		return previewFallback{Badge: "AUDIO", BadgeClass: "audio", Title: "Audio file", Text: "Album art was not available. Verify duplicates by exact hash, size, modified date, and path."}
	case isVideoExt(ext):
		return previewFallback{Badge: "VIDEO", BadgeClass: "video", Title: "Video file", Text: "Frame thumbnail was not available. Use Show In Explorer to play or inspect the file with a trusted application."}
	case isImageExt(ext):
		return previewFallback{Badge: "IMG", BadgeClass: "file", Title: "Image file", Text: "Image thumbnail could not be generated. Use Show In Explorer to inspect the original file."}
	default:
		return previewFallback{Badge: label, BadgeClass: "file", Title: "File preview", Text: "Generated metadata preview. Verify using exact hash, size, modified date, and path."}
	}
}

func fallbackForComparisonRow(row duplicateRow) previewFallback {
	if strings.EqualFold(filepath.Ext(row.File.Path), ".pdf") {
		return previewFallback{
			Badge:      "PDF",
			BadgeClass: "pdf",
			Title:      "PDF document",
			Text:       "Content is not copied into the temporary comparison page. Use Show In Explorer to inspect the document with a trusted application.",
		}
	}
	return fallbackForRow(row)
}

func writeFallbackPreview(builder *strings.Builder, fallback previewFallback) {
	builder.WriteString(`<div class="fallback"><div><span class="badge `)
	builder.WriteString(html.EscapeString(fallback.BadgeClass))
	builder.WriteString(`">`)
	builder.WriteString(html.EscapeString(fallback.Badge))
	builder.WriteString(`</span></div><div class="fallbackTitle">`)
	builder.WriteString(html.EscapeString(fallback.Title))
	builder.WriteString(`</div><div class="fallbackText">`)
	builder.WriteString(html.EscapeString(fallback.Text))
	builder.WriteString(`</div></div>`)
}

func buildComparisonPreviewPage(ctx context.Context, rows []duplicateRow, limit int, shellReady bool) (pagePath string, rendered int, tempDir string, err error) {
	if limit <= 0 {
		limit = maxComparisonPreviewFiles
	}
	rendered = len(rows)
	if rendered > limit {
		rendered = limit
	}

	if err := ctx.Err(); err != nil {
		return "", 0, "", err
	}
	tempDir, err = os.MkdirTemp(os.TempDir(), "twintidy-preview-")
	if err != nil {
		return "", 0, "", err
	}
	keepArtifact := false
	defer func() {
		if !keepArtifact {
			cleanupPreviewArtifact(tempDir)
			pagePath = ""
			tempDir = ""
		}
	}()

	type card struct {
		row       duplicateRow
		thumbPath string
		fallback  previewFallback
	}

	cards := make([]card, 0, rendered)
	for i := 0; i < rendered; i++ {
		if err := ctx.Err(); err != nil {
			return "", 0, tempDir, err
		}
		row := rows[i]
		item := card{row: row}
		if shellReady {
			thumb, thumbnailErr := shellThumbnailForVerifiedRecord(row.File, 420)
			if thumbnailErr == nil {
				thumbPath := filepath.Join(tempDir, fmt.Sprintf("thumb-%02d.png", i+1))
				file, createErr := os.OpenFile(thumbPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
				if createErr == nil {
					encodeErr := png.Encode(file, thumb)
					closeErr := file.Close()
					if encodeErr == nil && closeErr == nil {
						item.thumbPath = thumbPath
					} else {
						item.fallback = fallbackForComparisonRow(row)
					}
				} else {
					item.fallback = fallbackForComparisonRow(row)
				}
			} else {
				item.fallback = fallbackForComparisonRow(row)
			}
		} else {
			item.fallback = fallbackForComparisonRow(row)
		}
		cards = append(cards, item)
	}

	pagePath = filepath.Join(tempDir, "comparison.html")
	var builder strings.Builder
	builder.WriteString(`<!doctype html><html><head><meta http-equiv="X-UA-Compatible" content="IE=edge"><meta charset="utf-8"><style>
body{margin:0;background:#f5f6f8;color:#1d1d1f;font-family:-apple-system,BlinkMacSystemFont,"SF Pro Text","Segoe UI Variable Text","Segoe UI",Arial,sans-serif;font-size:13px;}
.notice{margin:14px;padding:10px 12px;background:#fff7e8;border:1px solid #f1d299;border-radius:8px;color:#5f3d00;}
.grid{padding:14px;}
.card{display:inline-block;vertical-align:top;width:260px;margin:0 12px 14px 0;background:rgba(255,255,255,.88);border:1px solid rgba(198,205,216,.72);border-radius:8px;box-shadow:0 10px 28px rgba(29,29,31,.08),0 1px 2px rgba(29,29,31,.06);overflow:hidden;}
.thumb{height:184px;margin:10px;background:#fff;border:1px solid #edf0f3;border-radius:7px;display:-ms-flexbox;display:flex;-ms-flex-align:center;align-items:center;-ms-flex-pack:center;justify-content:center;text-align:center;color:#6e6e73;}
.thumb img{max-width:100%;max-height:100%;object-fit:contain;}
.fallback{box-sizing:border-box;width:100%;height:100%;padding:18px 16px;display:flex;flex-direction:column;justify-content:center;text-align:left;background:linear-gradient(180deg,#ffffff,#f7f9fc);}
.badge{display:inline-block;min-width:38px;text-align:center;padding:4px 7px;margin-bottom:10px;border-radius:6px;background:#007aff;color:#fff;font-weight:700;letter-spacing:0;}
.badge.pdf{background:#ff3b30}.badge.office{background:#30b0c7}.badge.audio{background:#af52de}.badge.video{background:#ff9f0a}.badge.file{background:#6e6e73}
.fallbackTitle{font-weight:700;font-size:15px;margin-bottom:7px;color:#1d1d1f;}
.fallbackText{font-size:12px;line-height:16px;color:#6e6e73;max-height:96px;overflow:hidden;}
.body{padding:0 12px 12px 12px;}
.name{font-weight:600;font-size:14px;line-height:18px;word-break:break-word;margin-bottom:6px;}
.meta{color:#59606b;line-height:18px;word-break:break-word;}
.warning{margin-top:8px;color:#8a4b00;}
</style></head><body>`)

	if len(rows) > rendered {
		builder.WriteString(`<div class="notice">Showing first `)
		builder.WriteString(fmt.Sprint(rendered))
		builder.WriteString(` of `)
		builder.WriteString(fmt.Sprint(len(rows)))
		builder.WriteString(` selected files. Reduce the selection for side-by-side visual review, or use Show In Explorer for the full set.</div>`)
	}

	builder.WriteString(`<div class="grid">`)
	for _, item := range cards {
		row := item.row
		builder.WriteString(`<div class="card"><div class="thumb">`)
		if item.thumbPath != "" {
			builder.WriteString(`<img src="`)
			builder.WriteString(html.EscapeString(previewArtifactFileURL(item.thumbPath)))
			builder.WriteString(`" alt="">`)
		} else {
			writeFallbackPreview(&builder, item.fallback)
		}
		builder.WriteString(`</div><div class="body"><div class="name">`)
		builder.WriteString(html.EscapeString(displayFilesystemName(row.File.Path)))
		builder.WriteString(`</div><div class="meta">`)
		if row.Duplicate {
			builder.WriteString(`Group `)
			builder.WriteString(fmt.Sprint(row.Group))
		} else {
			builder.WriteString(html.EscapeString(row.Status))
		}
		builder.WriteString(` &middot; `)
		builder.WriteString(html.EscapeString(formatBytes(row.File.Size)))
		builder.WriteString(` &middot; `)
		builder.WriteString(html.EscapeString(displayUntrustedText(fileType(row.File.Path))))
		builder.WriteString(`<br>Modified: `)
		builder.WriteString(html.EscapeString(formatTime(row.File.ModifiedAt)))
		if row.Duplicate {
			builder.WriteString(`<br>Hash: `)
			builder.WriteString(html.EscapeString(shortHash(row.Hash)))
		}
		builder.WriteString(`</div>`)
		if row.File.Size > 512*1024*1024 {
			builder.WriteString(`<div class="warning">Large file: preview uses thumbnail and metadata only.</div>`)
		}
		if item.thumbPath == "" {
			builder.WriteString(`<div class="warning">Fallback preview generated by the app. Use Show In Explorer to inspect the file with a trusted application.</div>`)
		}
		builder.WriteString(`</div></div>`)
	}
	builder.WriteString(`</div></body></html>`)

	if err := ctx.Err(); err != nil {
		return "", 0, tempDir, err
	}
	if err := os.WriteFile(pagePath, []byte(builder.String()), 0o600); err != nil {
		return "", 0, tempDir, err
	}
	keepArtifact = true
	return pagePath, rendered, tempDir, nil
}

func comparisonSummary(rows []duplicateRow, rendered int) string {
	totalSize := int64(0)
	groups := make(map[int]struct{})
	duplicateRows := 0
	for _, row := range rows {
		totalSize += row.File.Size
		if row.Duplicate {
			duplicateRows++
			groups[row.Group] = struct{}{}
		}
	}

	var message string
	if duplicateRows > 0 {
		message = fmt.Sprintf("Comparison preview: %d selected file(s), including %d duplicate file(s) across %d duplicate group(s). Displaying %d preview card(s). Total selected size: %s.", len(rows), duplicateRows, len(groups), rendered, formatBytes(totalSize))
	} else {
		message = fmt.Sprintf("Comparison preview: %d selected scanned file(s). Displaying %d preview card(s). Total selected size: %s.", len(rows), rendered, formatBytes(totalSize))
	}
	if len(rows) > rendered {
		message += fmt.Sprintf("\r\n\r\nDisplay limit reached: only the first %d files are shown to keep the preview readable and responsive. Reduce the selection for a more detailed side-by-side comparison.", rendered)
	}
	message += "\r\n\r\nLarge files are shown with thumbnail, fallback preview, and metadata only. Absolute source paths and document text are not copied into the temporary comparison page."
	return message
}

func recycleOutcomeSummary(outcome recycleOutcome) string {
	status := fmt.Sprintf(
		"Recycle complete: %d recycled; %d changed and kept; %d protected and kept; %d cancelled and kept; %d failed and kept; %d request error(s).",
		outcome.recycled,
		outcome.changed,
		outcome.protected,
		outcome.cancelled,
		outcome.failed,
		len(outcome.requestErrors),
	)
	if outcome.internalError != "" {
		status += " An internal recycle workflow error was recorded."
	}
	return status
}

func recycleOutcomeHasIssues(outcome recycleOutcome) bool {
	return outcome.changed > 0 || outcome.protected > 0 || outcome.cancelled > 0 || outcome.failed > 0 || len(outcome.requestErrors) > 0 || outcome.internalError != ""
}

func recycleIssueSummary(outcome recycleOutcome) string {
	var builder strings.Builder
	if outcome.internalError != "" {
		builder.WriteString("Internal workflow error: ")
		builder.WriteString(displayUntrustedText(outcome.internalError))
		builder.WriteString("\r\n")
	}
	for _, requestError := range outcome.requestErrors {
		builder.WriteString(displayUntrustedText(requestError))
		builder.WriteString("\r\n")
	}
	for _, item := range outcome.nonRecycledRows {
		builder.WriteString(displayFilesystemPath(item.Path))
		builder.WriteString(": ")
		builder.WriteString(retainedRecycleStatus(item.Status))
		if item.Reason != "" {
			builder.WriteString(" - ")
			builder.WriteString(displayUntrustedText(item.Reason))
		}
		builder.WriteString("\r\n")
	}
	return builder.String()
}
