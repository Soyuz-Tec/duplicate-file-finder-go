# Standard Windows Go GUI Template

This is the standard template used for the Duplicate File Finder GUI approach.
Use it for Windows desktop apps that must build with the normal Go toolchain only.

## Core Decision

Use a Windows-native Go GUI instead of CGO-based frameworks.

- GUI framework: `github.com/lxn/walk`
- Windows API bindings: `github.com/lxn/win`
- Build mode: `CGO_ENABLED=0`
- No MSYS2
- No MinGW
- No GCC
- No GTK runtime

## When To Use This Template

Use this for apps that need:

- a native Windows desktop interface
- folder or file selection
- progress/status reporting
- long-running background work
- results tables
- preview/details panels
- safe confirmation before destructive actions
- crash/session logs
- a simple `.exe` build from Go

## Standard Project Shape

For a full production project, use this structure:

```text
cmd/appname/main.go
internal/gui/app_windows.go
internal/gui/app_stub.go
internal/core/engine.go
internal/core/types.go
internal/diagnostics/diagnostics.go
go.mod
README.md
.gitignore
.gitattributes
```

For a fast prototype or first version, the single-file starter below is enough.

## Required Dependencies

```powershell
go get github.com/lxn/walk@v0.0.0-20210112085537-c389da54e794
go get github.com/lxn/win@v0.0.0-20210218163916-a377121e959e
```

## Build Standard

```powershell
go env -w CGO_ENABLED=0
go test ./... -count=1
go build -ldflags="-H windowsgui" -o AppName.exe ./cmd/appname
```

For a single-file prototype:

```powershell
go env -w CGO_ENABLED=0
go run .\main.go
go build -ldflags="-H windowsgui" -o AppName.exe .\main.go
```

## GUI Layout Standard

Use this layout for most desktop tools:

```text
Main Window
  Toolbar
    App title
    Primary action
    Cancel
    Clear
    Reset
    Open Logs

  Status Panel
    Progress bar
    Stage
    Item count
    Elapsed time
    Current item
    Human-readable status

  Focus Panel
    Filters, switches, checkboxes, or scope controls

  Main Work Area
    Left: table or results list
    Right: preview/details panel

  Action Bar
    Smart selection
    Open item/location
    Export/report
    Confirm destructive action
```

## Workflow Rules

- Start with a clear source selection.
- Never begin risky work before the user chooses scope.
- Run long work in a goroutine.
- Update UI only through `mw.Synchronize`.
- Keep progress visible during and after work.
- Disable destructive buttons until valid targets exist.
- Confirm destructive actions with a modal dialog.
- Prefer reversible actions when possible.
- Keep logs available from the UI.
- Reset must return the app to a clean initial state.

## Single-File Starter App

Save this as `main.go` in a new app folder when starting a small Windows desktop app.

```go
//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

var (
	appBackground = walk.RGB(245, 246, 248)
	appPanel      = walk.RGB(251, 252, 254)
	appInk        = walk.RGB(29, 29, 31)
	appSecondary  = walk.RGB(103, 108, 118)
	appAccent     = walk.RGB(0, 122, 255)
	appDanger     = walk.RGB(255, 59, 48)
)

type app struct {
	mw *walk.MainWindow

	selectButton *walk.PushButton
	runButton    *walk.PushButton
	cancelButton *walk.PushButton
	clearButton  *walk.PushButton
	resetButton  *walk.PushButton
	logButton    *walk.PushButton
	deleteButton *walk.PushButton

	sourceLabel *walk.Label
	stageLabel  *walk.Label
	countLabel  *walk.Label
	timeLabel   *walk.Label
	statusLabel *walk.Label
	currentText *walk.TextEdit
	progress    *walk.ProgressBar
	table       *walk.TableView
	details     *walk.TextEdit

	model       *resultModel
	sourcePath  string
	running     bool
	startedAt   time.Time
	cancelWork  chan struct{}
	selectedRow int
}

type resultRow struct {
	Number int
	Status string
	Name   string
	Size   string
	Path   string
}

type resultModel struct {
	walk.TableModelBase
	rows []resultRow
}

func main() {
	a := &app{
		model:       &resultModel{},
		selectedRow: -1,
	}

	if err := a.create(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	a.reset()
	a.mw.Run()
}

func (a *app) create() error {
	return MainWindow{
		AssignTo: &a.mw,
		Title:    "Standard Go Windows App",
		MinSize:  Size{Width: 1000, Height: 650},
		Size:     Size{Width: 1280, Height: 760},
		Font:     Font{Family: "Segoe UI Variable Text", PointSize: 9},
		Layout:   VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}, Spacing: 10},
		Background: SolidColorBrush{
			Color: appBackground,
		},
		Children: []Widget{
			Composite{
				Layout: HBox{Spacing: 8},
				Children: []Widget{
					Label{
						Text:      "Standard Go Windows App",
						Font:      Font{Family: "Segoe UI Variable Display", PointSize: 13, Bold: true},
						TextColor: appInk,
					},
					HSpacer{},
					PushButton{AssignTo: &a.selectButton, Text: "Select Folder", OnClicked: a.selectFolder},
					PushButton{AssignTo: &a.runButton, Text: "Run", Background: SolidColorBrush{Color: appAccent}, OnClicked: a.run},
					PushButton{AssignTo: &a.cancelButton, Text: "Cancel", OnClicked: a.cancel},
					PushButton{AssignTo: &a.clearButton, Text: "Clear", OnClicked: a.clear},
					PushButton{AssignTo: &a.resetButton, Text: "Reset", OnClicked: a.reset},
					PushButton{AssignTo: &a.logButton, Text: "Open Logs", OnClicked: a.openLogs},
				},
			},
			Label{AssignTo: &a.sourceLabel, Text: "No folder selected", TextColor: appSecondary},
			GroupBox{
				Title:  "Status",
				Layout: VBox{Margins: Margins{Left: 12, Top: 10, Right: 12, Bottom: 10}, Spacing: 8},
				Background: SolidColorBrush{
					Color: appPanel,
				},
				Children: []Widget{
					ProgressBar{AssignTo: &a.progress, MinValue: 0, MaxValue: 1000},
					Composite{
						Layout: HBox{Spacing: 14},
						Children: []Widget{
							Label{AssignTo: &a.stageLabel, Text: "Stage: Idle"},
							Label{AssignTo: &a.countLabel, Text: "Items: 0"},
							Label{AssignTo: &a.timeLabel, Text: "Elapsed: 0s"},
						},
					},
					TextEdit{
						AssignTo: &a.currentText,
						ReadOnly: true,
						MaxSize:  Size{Height: 36},
					},
					Label{AssignTo: &a.statusLabel, Text: "Select a folder to start."},
				},
			},
			Composite{
				Layout: HBox{Spacing: 8},
				Children: []Widget{
					PushButton{Text: "Select All Except Newest", Enabled: false},
					PushButton{Text: "Clear Selection", Enabled: false},
					PushButton{Text: "Show In Explorer", Enabled: false},
					HSpacer{},
					PushButton{
						AssignTo:   &a.deleteButton,
						Text:       "Delete Selected",
						Background: SolidColorBrush{Color: appDanger},
						OnClicked:  a.confirmDelete,
					},
				},
			},
			HSplitter{
				Children: []Widget{
					TableView{
						AssignTo:                 &a.table,
						Model:                    a.model,
						AlternatingRowBG:         true,
						ColumnsOrderable:         true,
						ColumnsSizable:           true,
						LastColumnStretched:      true,
						MultiSelection:           false,
						OnCurrentIndexChanged:    a.rowChanged,
						OnSelectedIndexesChanged: a.rowChanged,
						Columns: []TableViewColumn{
							{Title: "No.", Width: 55},
							{Title: "Status", Width: 100},
							{Title: "Name", Width: 260},
							{Title: "Size", Width: 100},
							{Title: "Path", Width: 500},
						},
					},
					GroupBox{
						Title:  "Preview And Details",
						Layout: VBox{Margins: Margins{Left: 10, Top: 10, Right: 10, Bottom: 10}},
						Children: []Widget{
							TextEdit{
								AssignTo: &a.details,
								ReadOnly: true,
								Text:     "Select a result to see details.",
							},
						},
					},
				},
			},
		},
	}.Create()
}

func (a *app) selectFolder() {
	dialog := walk.FileDialog{
		Title: "Select folder",
	}

	ok, err := dialog.ShowBrowseFolder(a.mw)
	if err != nil {
		walk.MsgBox(a.mw, "Folder Selection Failed", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconError)
		return
	}
	if !ok {
		return
	}

	a.sourcePath = dialog.FilePath
	_ = a.sourceLabel.SetText(a.sourcePath)
	_ = a.currentText.SetText(a.sourcePath)
	a.runButton.SetEnabled(true)
}

func (a *app) run() {
	if a.running {
		return
	}
	if a.sourcePath == "" {
		walk.MsgBox(a.mw, "Select Folder", "Select a folder before running.", walk.MsgBoxOK|walk.MsgBoxIconInformation)
		return
	}

	a.running = true
	a.startedAt = time.Now()
	a.cancelWork = make(chan struct{})
	a.runButton.SetEnabled(false)
	a.cancelButton.SetEnabled(true)
	a.clearButton.SetEnabled(false)
	a.progress.SetValue(0)
	_ = a.stageLabel.SetText("Stage: Running")
	_ = a.statusLabel.SetText("Working...")

	go a.worker(a.sourcePath, a.cancelWork)
}

func (a *app) worker(root string, cancel <-chan struct{}) {
	rows := make([]resultRow, 0)
	count := 0

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		select {
		case <-cancel:
			return fmt.Errorf("cancelled")
		default:
		}
		if entry.IsDir() {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}

		count++
		row := resultRow{
			Number: count,
			Status: "Found",
			Name:   entry.Name(),
			Size:   formatBytes(info.Size()),
			Path:   path,
		}
		rows = append(rows, row)

		if count%25 == 0 {
			progress := count % 1000
			a.mw.Synchronize(func() {
				a.progress.SetValue(progress)
				_ = a.countLabel.SetText(fmt.Sprintf("Items: %d", count))
				_ = a.timeLabel.SetText("Elapsed: " + time.Since(a.startedAt).Round(time.Second).String())
				_ = a.currentText.SetText(path)
			})
		}
		return nil
	})

	a.mw.Synchronize(func() {
		a.running = false
		a.cancelButton.SetEnabled(false)
		a.runButton.SetEnabled(a.sourcePath != "")
		a.clearButton.SetEnabled(len(rows) > 0)
		a.progress.SetValue(1000)
		_ = a.stageLabel.SetText("Stage: Done")
		_ = a.countLabel.SetText(fmt.Sprintf("Items: %d", len(rows)))
		_ = a.timeLabel.SetText("Elapsed: " + time.Since(a.startedAt).Round(time.Second).String())

		if err != nil {
			_ = a.statusLabel.SetText("Work cancelled.")
			return
		}

		a.model.rows = rows
		a.model.PublishRowsReset()
		_ = a.table.Invalidate()
		_ = a.statusLabel.SetText(fmt.Sprintf("Complete. Found %d file(s).", len(rows)))
	})
}

func (a *app) cancel() {
	if a.running && a.cancelWork != nil {
		close(a.cancelWork)
	}
}

func (a *app) clear() {
	a.model.rows = nil
	a.model.PublishRowsReset()
	a.progress.SetValue(0)
	a.clearButton.SetEnabled(false)
	a.deleteButton.SetEnabled(false)
	_ = a.statusLabel.SetText("Results cleared.")
	_ = a.details.SetText("Select a result to see details.")
}

func (a *app) reset() {
	a.sourcePath = ""
	a.running = false
	a.selectedRow = -1
	a.model.rows = nil
	a.model.PublishRowsReset()
	a.progress.SetValue(0)
	a.runButton.SetEnabled(false)
	a.cancelButton.SetEnabled(false)
	a.clearButton.SetEnabled(false)
	a.deleteButton.SetEnabled(false)
	_ = a.sourceLabel.SetText("No folder selected")
	_ = a.stageLabel.SetText("Stage: Idle")
	_ = a.countLabel.SetText("Items: 0")
	_ = a.timeLabel.SetText("Elapsed: 0s")
	_ = a.currentText.SetText("")
	_ = a.statusLabel.SetText("Select a folder to start.")
	_ = a.details.SetText("Select a result to see details.")
}

func (a *app) rowChanged() {
	index := a.table.CurrentIndex()
	if index < 0 || index >= len(a.model.rows) {
		a.selectedRow = -1
		a.deleteButton.SetEnabled(false)
		_ = a.details.SetText("Select a result to see details.")
		return
	}

	a.selectedRow = index
	row := a.model.rows[index]
	a.deleteButton.SetEnabled(true)
	_ = a.details.SetText(fmt.Sprintf("Name: %s\r\nSize: %s\r\nPath: %s\r\nStatus: %s", row.Name, row.Size, row.Path, row.Status))
}

func (a *app) confirmDelete() {
	if a.selectedRow < 0 || a.selectedRow >= len(a.model.rows) {
		return
	}

	row := a.model.rows[a.selectedRow]
	result := walk.MsgBox(
		a.mw,
		"Confirm Action",
		"This template only demonstrates the confirmation pattern.\r\n\r\nSelected item:\r\n"+row.Path,
		walk.MsgBoxOKCancel|walk.MsgBoxIconWarning,
	)
	if result != 1 {
		return
	}

	_ = a.statusLabel.SetText("Confirmed action for: " + row.Name)
}

func (a *app) openLogs() {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "StandardGoWindowsApp", "logs")
	_ = os.MkdirAll(dir, 0o755)
	_ = walk.ShellExecute(a.mw, "open", dir, "", "", walk.SW_SHOWNORMAL)
}

func (m *resultModel) RowCount() int {
	return len(m.rows)
}

func (m *resultModel) Value(row, col int) interface{} {
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
		return item.Name
	case 3:
		return item.Size
	case 4:
		return item.Path
	default:
		return ""
	}
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
```

## Production Hardening Checklist

- Move business logic from the GUI into `internal/core`.
- Add unit tests for core logic.
- Add diagnostics logging under `%LOCALAPPDATA%\AppName\logs`.
- Add panic recovery around background workers.
- Add native folder/file dialogs where needed.
- Add table selection tests for destructive actions.
- Keep generated `.exe` files out of Git.
- Build with `CGO_ENABLED=0`.
- Run `go test ./... -count=1` before every release.
