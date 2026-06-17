//go:build windows

package gui

import (
	"context"
	"errors"
	"fmt"
	"html"
	"image/png"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"duplicate-file-finder-go/internal/diagnostics"
	"duplicate-file-finder-go/internal/scanner"

	pdf "github.com/ledongthuc/pdf"
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

	selectFolderButton *walk.PushButton
	scanButton         *walk.PushButton
	cancelButton       *walk.PushButton
	clearButton        *walk.PushButton
	resetButton        *walk.PushButton
	logButton          *walk.PushButton
	keepNewestButton   *walk.PushButton
	keepOldestButton   *walk.PushButton
	clearSelectButton  *walk.PushButton
	openButton         *walk.PushButton
	richPreviewButton  *walk.PushButton
	deleteButton       *walk.PushButton

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

	selectedFolder string
	surfaceReady   bool
	surfaceReport  scanner.SurfaceReport
	scanCancel     context.CancelFunc
	scanStartedAt  time.Time
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

func Run() {
	diagnostics.Logf("gui initializing")
	ui := &windowsApp{
		engine: scanner.NewEngine(0),
		model:  &duplicateTableModel{},
	}
	ui.model.onSelectionChanged = ui.updateDeleteActionState
	if err := ui.create(); err != nil {
		diagnostics.Logf("gui create failed: %v", err)
		fmt.Fprintln(os.Stderr, err)
		return
	}
	ui.setInitialState()
	diagnostics.Logf("gui ready")
	ui.mw.Run()
}

func (a *windowsApp) create() error {
	return MainWindow{
		AssignTo: &a.mw,
		Title:    "Duplicate File Finder",
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
					Label{Text: "Duplicate File Finder", Font: macTitleFont(), TextColor: macInk, MinSize: Size{Width: 190}},
					HSpacer{},
					PushButton{AssignTo: &a.selectFolderButton, Text: "Select Folder", Font: macControlFont(), OnClicked: a.selectFolder},
					PushButton{AssignTo: &a.scanButton, Text: "Scan", Font: macControlFont(), Background: SolidColorBrush{Color: macAccentBlue}, OnClicked: a.startScan},
					PushButton{AssignTo: &a.cancelButton, Text: "Cancel", Font: macControlFont(), OnClicked: a.cancelScan},
					PushButton{AssignTo: &a.clearButton, Text: "Clear Results", Font: macControlFont(), OnClicked: a.clearResults},
					PushButton{AssignTo: &a.resetButton, Text: "Reset", Font: macControlFont(), OnClicked: a.resetAll},
					PushButton{AssignTo: &a.logButton, Text: "Open Logs", Font: macControlFont(), OnClicked: a.openLogs},
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
					PushButton{AssignTo: &a.richPreviewButton, Text: "Rich Preview", Font: macControlFont(), OnClicked: a.showSelectedRichPreview},
					HSpacer{},
					PushButton{AssignTo: &a.deleteButton, Text: "Delete Selected", Font: macControlFont(), Background: SolidColorBrush{Color: macDangerRed}, OnClicked: a.confirmDeleteSelected},
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
								AssignTo:         &a.webPreview,
								Background:       SolidColorBrush{Color: walk.RGB(255, 255, 255)},
								Visible:          false,
								MinSize:          Size{Width: 500, Height: 430},
								StretchFactor:    4,
								ShortcutsEnabled: true,
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
	_ = a.scanButton.SetText("Surface Scan")
	a.scanButton.SetEnabled(false)
	a.cancelButton.SetEnabled(false)
	a.clearButton.SetEnabled(false)
	a.keepNewestButton.SetEnabled(false)
	a.keepOldestButton.SetEnabled(false)
	a.clearSelectButton.SetEnabled(false)
	a.openButton.SetEnabled(false)
	a.richPreviewButton.SetEnabled(false)
	a.deleteButton.SetEnabled(false)
	a.clearFileFocus()
	a.setPreviewMessage("Select a duplicate row to show an Explorer-style visual preview and verification details.")
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
		item.check.SetEnabled(stats.Files > 0)
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
	path, accepted, err := showModernFolderDialog(a.mw, "Select folder to scan", a.selectedFolder)
	if err != nil {
		diagnostics.Logf("modern folder picker failed; falling back to legacy picker: %v", err)
		dlg := walk.FileDialog{
			Title:          "Select folder to scan",
			InitialDirPath: a.selectedFolder,
		}
		accepted, err = dlg.ShowBrowseFolder(a.mw)
		if err != nil {
			walk.MsgBox(a.mw, "Folder Selection Failed", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconError)
			return
		}
		path = dlg.FilePath
	}
	if !accepted {
		return
	}
	a.selectedFolder = path
	a.surfaceReady = false
	a.surfaceReport = scanner.SurfaceReport{}
	a.clearFileFocus()
	diagnostics.Logf("folder selected: %q", a.selectedFolder)
	_ = a.folderLabel.SetText(a.selectedFolder)
	_ = a.currentLabel.SetText(a.selectedFolder)
	_ = a.statusLabel.SetText("Ready to scan.")
	_ = a.scanButton.SetText("Surface Scan")
	a.scanButton.SetEnabled(true)
}

func (a *windowsApp) startScan() {
	if a.selectedFolder == "" {
		walk.MsgBox(a.mw, "Select Folder", "Choose a folder before scanning.", walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}

	if !a.surfaceReady {
		a.startSurfaceScan()
		return
	}

	a.startDuplicateScan()
}

func (a *windowsApp) startSurfaceScan() {
	a.model.setRows(nil)
	a.publishRows()
	a.progress.SetValue(0)
	_ = a.stageLabel.SetText(string(scanner.StageSurfaceScan))
	_ = a.filesLabel.SetText("0 files")
	_ = a.elapsedLabel.SetText("0s")
	_ = a.currentLabel.SetText(a.selectedFolder)
	_ = a.statusLabel.SetText("Starting surface scan.")
	a.setPreviewMessage("Scanning user-created files only. System, app, executable, and dependency files are skipped.")

	a.scanButton.SetEnabled(false)
	a.cancelButton.SetEnabled(true)
	a.clearButton.SetEnabled(false)
	a.setResultActionsEnabled(false)
	a.setFileFocusEnabled(false)

	ctx, cancel := context.WithCancel(context.Background())
	a.scanCancel = cancel
	a.scanStartedAt = time.Now()
	folder := a.selectedFolder
	diagnostics.Logf("surface scan started: folder=%q", folder)

	updates := make(chan scanner.Progress, 512)
	done := make(chan struct{})

	go func() {
		defer func() {
			if err := diagnostics.RecoverToError("gui progress updater", map[string]string{"folder": folder}); err != nil {
				diagnostics.Logf("%v", err)
			}
		}()
		a.consumeProgress(updates)
	}()
	go func() {
		defer func() {
			if err := diagnostics.RecoverToError("gui elapsed timer", map[string]string{"folder": folder}); err != nil {
				diagnostics.Logf("%v", err)
			}
		}()
		a.tickElapsed(ctx, done, a.scanStartedAt)
	}()
	go func() {
		var report scanner.SurfaceReport
		var err error
		defer func() {
			if crashErr := diagnostics.RecoverToError("surface scan workflow", map[string]string{"folder": folder}); crashErr != nil {
				err = crashErr
			}
			diagnostics.Logf("surface scan ended: folder=%q files=%d err=%v", folder, report.TotalFiles, err)
			close(updates)
			close(done)
			a.mw.Synchronize(func() {
				a.surfaceScanFinished(report, err)
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

	a.model.setRows(nil)
	a.publishRows()
	a.progress.SetValue(0)
	_ = a.stageLabel.SetText(string(scanner.StageSizeMapping))
	_ = a.filesLabel.SetText("0 files")
	_ = a.elapsedLabel.SetText("0s")
	_ = a.currentLabel.SetText(a.selectedFolder)
	_ = a.statusLabel.SetText("Finding exact duplicates in selected user file types.")
	a.setPreviewMessage("Finding exact duplicates in the selected user-created file categories.")

	a.scanButton.SetEnabled(false)
	a.cancelButton.SetEnabled(true)
	a.clearButton.SetEnabled(false)
	a.setResultActionsEnabled(false)
	a.setFileFocusEnabled(false)

	ctx, cancel := context.WithCancel(context.Background())
	a.scanCancel = cancel
	a.scanStartedAt = time.Now()
	folder := a.selectedFolder
	diagnostics.Logf("duplicate scan started: folder=%q categories=%s", folder, categorySelectionSummary(categories))

	updates := make(chan scanner.Progress, 512)
	done := make(chan struct{})

	go func() {
		defer func() {
			if err := diagnostics.RecoverToError("gui progress updater", map[string]string{"folder": folder}); err != nil {
				diagnostics.Logf("%v", err)
			}
		}()
		a.consumeProgress(updates)
	}()
	go func() {
		defer func() {
			if err := diagnostics.RecoverToError("gui elapsed timer", map[string]string{"folder": folder}); err != nil {
				diagnostics.Logf("%v", err)
			}
		}()
		a.tickElapsed(ctx, done, a.scanStartedAt)
	}()
	go func() {
		var groups []scanner.DuplicateGroup
		var err error
		defer func() {
			if crashErr := diagnostics.RecoverToError("duplicate scan workflow", map[string]string{"folder": folder}); crashErr != nil {
				err = crashErr
			}
			diagnostics.Logf("duplicate scan ended: folder=%q groups=%d err=%v", folder, len(groups), err)
			close(updates)
			close(done)
			a.mw.Synchronize(func() {
				a.scanFinished(groups, err)
			})
		}()

		groups, err = a.engine.ScanFiles(ctx, a.surfaceReport.Files, scanner.ScanOptions{
			Categories:    categories,
			UserFilesOnly: true,
		}, updates)
	}()
}

func (a *windowsApp) consumeProgress(updates <-chan scanner.Progress) {
	for progress := range updates {
		p := progress
		a.mw.Synchronize(func() {
			_ = a.stageLabel.SetText(string(p.Stage))
			if p.CurrentPath != "" {
				_ = a.currentLabel.SetText(p.CurrentPath)
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

func (a *windowsApp) tickElapsed(ctx context.Context, done <-chan struct{}, startedAt time.Time) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			elapsed := formatDuration(time.Since(startedAt))
			a.mw.Synchronize(func() {
				_ = a.elapsedLabel.SetText(elapsed)
			})
		}
	}
}

func (a *windowsApp) surfaceScanFinished(report scanner.SurfaceReport, err error) {
	a.scanCancel = nil
	a.cancelButton.SetEnabled(false)
	a.scanButton.SetEnabled(a.selectedFolder != "")

	if err != nil {
		if errors.Is(err, context.Canceled) {
			diagnostics.Logf("surface scan canceled")
			_ = a.statusLabel.SetText("Surface scan canceled.")
			_ = a.scanButton.SetText("Surface Scan")
			return
		}
		diagnostics.Logf("surface scan failed: %v", err)
		_ = a.statusLabel.SetText("Surface scan failed.")
		_ = a.scanButton.SetText("Surface Scan")
		walk.MsgBox(a.mw, "Surface Scan Failed", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconError)
		return
	}

	a.surfaceReport = report
	a.surfaceReady = true
	a.updateFileFocusFromSurfaceReport()
	a.model.setSurfaceFiles(report.Files, "Scanned")
	a.publishRows()
	a.progress.SetValue(1000)
	_ = a.stageLabel.SetText(string(scanner.StageSurfaceScan))
	_ = a.filesLabel.SetText(fmt.Sprintf("%d user files", report.TotalFiles))
	_ = a.elapsedLabel.SetText(formatDuration(time.Since(a.scanStartedAt)))
	_ = a.statusLabel.SetText(fmt.Sprintf("Surface scan complete. Found %d user-created file(s), skipped %d system/app item(s).", report.TotalFiles, report.SkippedSystemItems))
	_ = a.scanButton.SetText("Find Duplicates")
	a.clearButton.SetEnabled(len(a.model.rows) > 0)
	a.setResultActionsEnabled(false)
	a.setPreviewMessage("Surface scan complete. The table lists all eligible user-created files. Select file types, then run duplicate scan.")
	diagnostics.Logf("surface results published: files=%d skipped=%d", report.TotalFiles, report.SkippedSystemItems)
}

func (a *windowsApp) scanFinished(groups []scanner.DuplicateGroup, err error) {
	a.scanCancel = nil
	a.cancelButton.SetEnabled(false)
	a.scanButton.SetEnabled(a.selectedFolder != "")
	a.setFileFocusEnabled(a.surfaceReady)
	if a.surfaceReady {
		_ = a.scanButton.SetText("Find Duplicates")
	} else {
		_ = a.scanButton.SetText("Surface Scan")
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			diagnostics.Logf("scan canceled")
			_ = a.statusLabel.SetText("Scan canceled.")
			return
		}
		diagnostics.Logf("scan failed: %v", err)
		_ = a.statusLabel.SetText("Scan failed.")
		walk.MsgBox(a.mw, "Scan Failed", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconError)
		return
	}

	if len(groups) == 0 {
		a.model.setSurfaceFiles(a.surfaceFilesForSelectedCategories(), "No duplicate")
	} else {
		a.model.setGroups(groups)
	}
	diagnostics.Logf("scan results published: groups=%d rows=%d", len(groups), len(a.model.rows))
	a.publishRows()
	a.progress.SetValue(1000)
	_ = a.stageLabel.SetText(string(scanner.StageDone))
	_ = a.elapsedLabel.SetText(formatDuration(time.Since(a.scanStartedAt)))
	if len(groups) == 0 {
		_ = a.statusLabel.SetText(fmt.Sprintf("No exact duplicates found. Showing %d scanned file(s) from the selected focus categories.", len(a.model.rows)))
		a.setPreviewMessage("No exact duplicates were found. The table remains populated for review, preview, and opening file locations.")
	} else {
		_ = a.statusLabel.SetText(fmt.Sprintf("Found %d exact duplicate group(s), containing %d duplicate file record(s).", len(groups), len(a.model.rows)))
		a.setPreviewMessage("Select a row to inspect file details, exact hash, and a visual preview for supported file types.")
	}

	a.clearButton.SetEnabled(len(a.model.rows) > 0)
	a.setResultActionsEnabled(len(groups) > 0)
	a.updateDeleteActionState()
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
	if a.scanCancel != nil {
		a.scanCancel()
	}
}

func (a *windowsApp) clearResults() {
	a.model.setRows(nil)
	a.publishRows()
	a.progress.SetValue(0)
	if a.surfaceReady {
		_ = a.stageLabel.SetText(string(scanner.StageSurfaceScan))
		_ = a.statusLabel.SetText("Duplicate results cleared. Adjust file focus or run duplicate scan again.")
		_ = a.scanButton.SetText("Find Duplicates")
		a.scanButton.SetEnabled(true)
		a.setFileFocusEnabled(true)
	} else {
		_ = a.stageLabel.SetText(string(scanner.StageIdle))
		_ = a.statusLabel.SetText("Results cleared.")
		_ = a.scanButton.SetText("Surface Scan")
		a.scanButton.SetEnabled(a.selectedFolder != "")
	}
	_ = a.filesLabel.SetText("0 files")
	_ = a.currentLabel.SetText(a.selectedFolder)
	a.setPreviewMessage("Results cleared. Start a scan to find exact duplicates.")
	a.clearButton.SetEnabled(false)
	a.setResultActionsEnabled(false)
}

func (a *windowsApp) resetAll() {
	a.cancelScan()
	a.selectedFolder = ""
	a.surfaceReady = false
	a.surfaceReport = scanner.SurfaceReport{}
	a.model.setRows(nil)
	a.publishRows()
	a.progress.SetValue(0)
	_ = a.folderLabel.SetText("No folder selected")
	_ = a.stageLabel.SetText(string(scanner.StageIdle))
	_ = a.filesLabel.SetText("0 files")
	_ = a.elapsedLabel.SetText("0s")
	_ = a.currentLabel.SetText("-")
	_ = a.statusLabel.SetText("Select a folder to start.")
	_ = a.scanButton.SetText("Surface Scan")
	a.clearFileFocus()
	a.setPreviewMessage("Select a duplicate row to show an Explorer-style visual preview and verification details.")
	a.scanButton.SetEnabled(false)
	a.cancelButton.SetEnabled(false)
	a.clearButton.SetEnabled(false)
	a.setResultActionsEnabled(false)
}

func (a *windowsApp) openLogs() {
	dir := diagnostics.LogDir()
	if dir == "" {
		walk.MsgBox(a.mw, "Logs", "Diagnostics logging is not initialized for this run.", walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		walk.MsgBox(a.mw, "Logs", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconError)
		return
	}
	if err := exec.Command("explorer.exe", dir).Start(); err != nil {
		walk.MsgBox(a.mw, "Logs", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconError)
	}
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
	_ = exec.Command("explorer.exe", "/select,"+path).Start()
}

func (a *windowsApp) confirmDeleteSelected() {
	paths := a.deleteCandidatePaths()
	if len(paths) == 0 {
		walk.MsgBox(a.mw, "No Selection", "Select duplicate files before deleting.", walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}

	message := fmt.Sprintf("Move %d selected duplicate file(s) to the Recycle Bin? If the Recycle Bin is unavailable, the app will permanently delete the selected files.", len(paths))
	result := walk.MsgBox(a.mw, "Delete Selected Files", message, walk.MsgBoxYesNo|walk.MsgBoxIconWarning)
	if result != win.IDYES {
		return
	}

	a.deleteButton.SetEnabled(false)
	_ = a.statusLabel.SetText("Deleting selected files.")
	diagnostics.Logf("delete started: selected=%d", len(paths))
	go func(paths []string) {
		deleteResult := scanner.DeleteResult{}
		defer func() {
			if crashErr := diagnostics.RecoverToError("delete workflow", map[string]string{"selected_files": fmt.Sprint(len(paths))}); crashErr != nil {
				deleteResult.Failed = append(deleteResult.Failed, scanner.DeleteFailure{Error: crashErr.Error()})
			}
			diagnostics.Logf("delete ended: selected=%d deleted=%d failed=%d", len(paths), len(deleteResult.Deleted), len(deleteResult.Failed))
			a.mw.Synchronize(func() {
				a.applyDeleteResult(deleteResult)
			})
		}()

		deleteResult = scanner.DeleteFiles(paths, true)
	}(paths)
}

func (a *windowsApp) applyDeleteResult(result scanner.DeleteResult) {
	deleted := make(map[string]struct{}, len(result.Deleted))
	trashCount := 0
	permanentCount := 0
	for _, file := range result.Deleted {
		deleted[file.Path] = struct{}{}
		if file.Action == scanner.DeleteActionTrash {
			trashCount++
		} else {
			permanentCount++
		}
	}

	a.model.removeDeleted(deleted)
	if a.table != nil {
		_ = a.table.SetSelectedIndexes(nil)
		_ = a.table.SetCurrentIndex(-1)
	}
	a.publishRows()
	a.progress.SetValue(1000)
	a.clearButton.SetEnabled(len(a.model.rows) > 0)
	a.setResultActionsEnabled(len(a.model.rows) > 0)
	a.updateDeleteActionState()

	status := fmt.Sprintf("Deleted %d file(s): %d moved to Recycle Bin, %d permanently removed.", len(result.Deleted), trashCount, permanentCount)
	if len(result.Failed) > 0 {
		status = fmt.Sprintf("%s %d file(s) could not be deleted.", status, len(result.Failed))
		walk.MsgBox(a.mw, "Some Files Were Not Deleted", failureSummary(result.Failed), walk.MsgBoxOK|walk.MsgBoxIconWarning)
	}
	_ = a.statusLabel.SetText(status)
	if len(a.model.rows) == 0 {
		a.setPreviewMessage("No duplicate groups remain after deletion.")
	}
}

func (a *windowsApp) updatePreviewFromSelection() {
	a.updateDeleteActionState()

	selectedRows := a.selectedRows()
	if len(selectedRows) > 1 {
		a.openButton.SetEnabled(true)
		a.richPreviewButton.SetEnabled(false)
		a.showComparisonPreview(selectedRows)
		return
	}

	index := a.table.CurrentIndex()
	if index < 0 || index >= len(a.model.rows) {
		a.setPreviewMessage("Select a duplicate row to show an Explorer-style visual preview and verification details.")
		a.openButton.SetEnabled(false)
		a.richPreviewButton.SetEnabled(false)
		return
	}
	a.openButton.SetEnabled(true)
	row := a.model.rows[index]
	ext := strings.ToLower(filepath.Ext(row.File.Path))
	richSupported := isRichPreviewExt(ext)
	a.richPreviewButton.SetEnabled(richSupported)
	if richSupported {
		a.showRichPreviewForRow(row)
		return
	}
	a.showPreviewForRow(row)
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

func (a *windowsApp) highlightedDuplicatePaths() []string {
	rows := a.selectedRows()
	paths := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if !row.Duplicate {
			continue
		}
		if _, ok := seen[row.File.Path]; ok {
			continue
		}
		seen[row.File.Path] = struct{}{}
		paths = append(paths, row.File.Path)
	}
	sort.Strings(paths)
	return paths
}

func (a *windowsApp) deleteCandidatePaths() []string {
	paths := a.model.selectedPaths()
	if len(paths) > 0 {
		return paths
	}
	return a.highlightedDuplicatePaths()
}

func (a *windowsApp) updateDeleteActionState() {
	if a.deleteButton == nil {
		return
	}
	a.deleteButton.SetEnabled(len(a.deleteCandidatePaths()) > 0)
}

func (a *windowsApp) setResultActionsEnabled(enabled bool) {
	a.keepNewestButton.SetEnabled(enabled)
	a.keepOldestButton.SetEnabled(enabled)
	a.clearSelectButton.SetEnabled(enabled)
	a.openButton.SetEnabled(enabled)
	a.richPreviewButton.SetEnabled(false)
	if enabled {
		a.updateDeleteActionState()
		return
	}
	a.deleteButton.SetEnabled(false)
}

func (a *windowsApp) publishRows() {
	a.model.PublishRowsReset()
	_ = a.table.Invalidate()
}

func (a *windowsApp) showPreviewForRow(row duplicateRow) {
	details := previewDetailsForRow(row)
	ext := strings.ToLower(filepath.Ext(row.File.Path))
	if isImageExt(ext) {
		image, err := walk.NewImageFromFileForDPI(row.File.Path, a.previewImage.DPI())
		if err == nil {
			a.setPreviewImage(image)
			_ = a.previewText.SetText(details)
			return
		}
		details += "\r\n\r\nVisual preview failed: " + err.Error()
	}

	if image, err := shellThumbnailImage(row.File.Path, 1200, a.previewImage.DPI()); err == nil {
		a.setPreviewImage(image)
		if isRichPreviewExt(ext) {
			details += "\r\n\r\nExplorer-style thumbnail loaded. Use Rich Preview for an embedded document/media view when Windows can render this file type."
		} else {
			details += "\r\n\r\nExplorer-style thumbnail loaded through the Windows Shell provider."
		}
		_ = a.previewText.SetText(details)
		return
	}

	a.setPreviewImage(nil)
	if isTextExt(ext) {
		text, err := readTextPreview(row.File.Path)
		if err != nil {
			details += "\r\n\r\nText preview failed: " + err.Error()
		} else {
			details += "\r\n\r\nText preview:\r\n" + text
		}
	} else {
		fallback := fallbackForRow(row)
		details += "\r\n\r\n" + fallback.Title + ":\r\n" + fallback.Text
		details += "\r\n\r\nThis is an app-generated fallback preview. Use Show In Explorer for the file location, or Rich Preview when this file type has a local viewer."
	}
	_ = a.previewText.SetText(details)
}

func (a *windowsApp) setPreviewMessage(message string) {
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

func (a *windowsApp) showComparisonPreview(rows []duplicateRow) {
	a.setPreviewImage(nil)
	a.previewImage.SetVisible(false)
	a.webPreview.SetVisible(true)

	page, rendered, err := buildComparisonPreviewPage(rows, maxComparisonPreviewFiles)
	if err != nil {
		a.webPreview.SetVisible(false)
		a.previewImage.SetVisible(true)
		_ = a.previewText.SetText("Comparison preview failed: " + err.Error())
		return
	}

	_ = a.previewText.SetText(comparisonSummary(rows, rendered))
	if err := a.webPreview.SetURL(fileURL(page)); err != nil {
		a.webPreview.SetVisible(false)
		a.previewImage.SetVisible(true)
		_ = a.previewText.SetText("Comparison preview failed: " + err.Error())
	}
}

func (a *windowsApp) showSelectedRichPreview() {
	index := a.table.CurrentIndex()
	if index < 0 || index >= len(a.model.rows) {
		walk.MsgBox(a.mw, "Select A File", "Select a row first.", walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}
	a.showRichPreviewForRow(a.model.rows[index])
}

func (a *windowsApp) showRichPreviewForRow(row duplicateRow) {
	ext := strings.ToLower(filepath.Ext(row.File.Path))
	if !isRichPreviewExt(ext) {
		walk.MsgBox(a.mw, "Rich Preview", "This file type does not have an embedded rich preview path in this app.", walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}

	a.setPreviewImage(nil)
	a.previewImage.SetVisible(false)
	a.webPreview.SetVisible(true)
	_ = a.previewText.SetText(previewDetailsForRow(row) + "\r\n\r\nEmbedded rich preview requested. Rendering depends on Windows, Office/PDF/video handlers, and the local browser control available on this machine.")
	if err := a.webPreview.SetURL(fileURL(row.File.Path)); err != nil {
		a.webPreview.SetVisible(false)
		a.previewImage.SetVisible(true)
		_ = a.previewText.SetText(previewDetailsForRow(row) + "\r\n\r\nRich preview failed: " + err.Error())
	}
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
		return filepath.Base(item.File.Path)
	case 4:
		return formatBytes(item.File.Size)
	case 5:
		return fileType(item.File.Path)
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
		return item.File.Path
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
	builder.WriteString(filepath.Base(row.File.Path))
	builder.WriteString("\r\nCategory: ")
	builder.WriteString(scanner.CategoryLabel(row.File.Category))
	builder.WriteString("\r\nType: ")
	builder.WriteString(fileType(row.File.Path))
	builder.WriteString("\r\nSize: ")
	builder.WriteString(formatBytes(row.File.Size))
	builder.WriteString("\r\nCreated: ")
	builder.WriteString(formatTime(row.File.CreatedAt))
	builder.WriteString("\r\nModified: ")
	builder.WriteString(formatTime(row.File.ModifiedAt))
	builder.WriteString("\r\nPath: ")
	builder.WriteString(row.File.Path)
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

func isRichPreviewExt(ext string) bool {
	switch ext {
	case ".pdf",
		".doc", ".docx", ".docm", ".rtf",
		".xls", ".xlsx", ".xlsm", ".csv",
		".ppt", ".pptx", ".pptm",
		".mp3", ".m4a", ".aac", ".wav", ".wma", ".flac", ".ogg",
		".mp4", ".m4v", ".mov", ".avi", ".mkv", ".wmv", ".webm", ".mpeg", ".mpg":
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

func readTextPreview(path string) (string, error) {
	file, err := os.Open(path)
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

func fileURL(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	return (&url.URL{Scheme: "file", Path: clean}).String()
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

	switch {
	case ext == ".pdf":
		text, pages, err := pdfPreviewSnippet(row.File.Path)
		title := "PDF document"
		if pages > 0 {
			title = fmt.Sprintf("PDF document - %d page(s)", pages)
		}
		if err != nil || text == "" {
			text = "PDF preview card generated from file metadata. Use Rich Preview to inspect the full document when the local PDF viewer is available."
		}
		return previewFallback{Badge: "PDF", BadgeClass: "pdf", Title: title, Text: text}
	case isOfficeExt(ext):
		return previewFallback{Badge: label, BadgeClass: "office", Title: "Office document", Text: "Document thumbnail was not available. Use Rich Preview to inspect the file with local Office or browser handlers."}
	case isAudioExt(ext):
		return previewFallback{Badge: "AUDIO", BadgeClass: "audio", Title: "Audio file", Text: "Album art was not available. Verify duplicates by exact hash, size, modified date, and path."}
	case isVideoExt(ext):
		return previewFallback{Badge: "VIDEO", BadgeClass: "video", Title: "Video file", Text: "Frame thumbnail was not available. Use Rich Preview or Show In Explorer to play or inspect the file."}
	case isImageExt(ext):
		return previewFallback{Badge: "IMG", BadgeClass: "file", Title: "Image file", Text: "Image thumbnail could not be generated. Use Show In Explorer to inspect the original file."}
	default:
		return previewFallback{Badge: label, BadgeClass: "file", Title: "File preview", Text: "Generated metadata preview. Verify using exact hash, size, modified date, and path."}
	}
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

func pdfPreviewSnippet(path string) (snippet string, pages int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			snippet = ""
			pages = 0
			err = fmt.Errorf("PDF preview parser failed: %v", recovered)
		}
	}()

	file, reader, err := pdf.Open(path)
	if file != nil {
		defer file.Close()
	}
	if err != nil {
		return "", 0, err
	}

	pages = reader.NumPage()
	if pages == 0 {
		return "", 0, nil
	}

	fonts := make(map[string]*pdf.Font)
	pageLimit := pages
	if pageLimit > 2 {
		pageLimit = 2
	}

	var builder strings.Builder
	for pageIndex := 1; pageIndex <= pageLimit; pageIndex++ {
		page := reader.Page(pageIndex)
		if page.V.IsNull() || page.V.Key("Contents").Kind() == pdf.Null {
			continue
		}
		for _, name := range page.Fonts() {
			if _, ok := fonts[name]; !ok {
				font := page.Font(name)
				fonts[name] = &font
			}
		}
		text, err := page.GetPlainText(fonts)
		if err != nil {
			continue
		}
		builder.WriteString(text)
		builder.WriteString(" ")
		if builder.Len() > 600 {
			break
		}
	}

	return truncatePreviewText(builder.String(), 360), pages, nil
}

func truncatePreviewText(text string, limit int) string {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return ""
	}
	normalized := strings.Join(parts, " ")
	if len(normalized) <= limit {
		return normalized
	}
	if limit <= 3 {
		return normalized[:limit]
	}
	return normalized[:limit-3] + "..."
}

func buildComparisonPreviewPage(rows []duplicateRow, limit int) (string, int, error) {
	if limit <= 0 {
		limit = maxComparisonPreviewFiles
	}
	rendered := len(rows)
	if rendered > limit {
		rendered = limit
	}

	dir := filepath.Join(os.TempDir(), "duplicate-file-finder-preview")
	if err := os.RemoveAll(dir); err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}

	type card struct {
		row       duplicateRow
		thumbPath string
		fallback  previewFallback
	}

	cards := make([]card, 0, rendered)
	for i := 0; i < rendered; i++ {
		row := rows[i]
		item := card{row: row}
		if thumb, err := shellThumbnailNRGBA(row.File.Path, 420); err == nil {
			thumbPath := filepath.Join(dir, fmt.Sprintf("thumb-%02d.png", i+1))
			file, createErr := os.Create(thumbPath)
			if createErr == nil {
				encodeErr := png.Encode(file, thumb)
				closeErr := file.Close()
				if encodeErr == nil && closeErr == nil {
					item.thumbPath = thumbPath
				} else if encodeErr != nil {
					item.fallback = fallbackForRow(row)
				} else {
					item.fallback = fallbackForRow(row)
				}
			} else {
				item.fallback = fallbackForRow(row)
			}
		} else {
			item.fallback = fallbackForRow(row)
		}
		cards = append(cards, item)
	}

	pagePath := filepath.Join(dir, "comparison.html")
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
.path{margin-top:8px;color:#858b96;font-size:12px;word-break:break-all;}
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
			builder.WriteString(html.EscapeString(fileURL(item.thumbPath)))
			builder.WriteString(`" alt="">`)
		} else {
			writeFallbackPreview(&builder, item.fallback)
		}
		builder.WriteString(`</div><div class="body"><div class="name">`)
		builder.WriteString(html.EscapeString(filepath.Base(row.File.Path)))
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
		builder.WriteString(html.EscapeString(fileType(row.File.Path)))
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
			builder.WriteString(`<div class="warning">Fallback preview generated by the app. Use Rich Preview for a full document/media view.</div>`)
		}
		builder.WriteString(`<div class="path">`)
		builder.WriteString(html.EscapeString(row.File.Path))
		builder.WriteString(`</div></div></div>`)
	}
	builder.WriteString(`</div></body></html>`)

	if err := os.WriteFile(pagePath, []byte(builder.String()), 0o644); err != nil {
		return "", 0, err
	}
	return pagePath, rendered, nil
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
	message += "\r\n\r\nLarge files are shown with thumbnail, fallback preview, and metadata only. Files without Windows thumbnails use app-generated fallback cards with metadata and PDF text snippets when available."
	return message
}

func failureSummary(failures []scanner.DeleteFailure) string {
	var builder strings.Builder
	limit := len(failures)
	if limit > 8 {
		limit = 8
	}
	for i := 0; i < limit; i++ {
		builder.WriteString(failures[i].Path)
		builder.WriteString(": ")
		builder.WriteString(failures[i].Error)
		builder.WriteString("\r\n")
	}
	if len(failures) > limit {
		builder.WriteString(fmt.Sprintf("...and %d more.", len(failures)-limit))
	}
	return builder.String()
}
