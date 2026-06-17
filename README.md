# Duplicate File Finder Go

High-performance Windows desktop duplicate finder written in Go. The scanner uses a three-stage pipeline:

1. Group files by exact byte size.
2. Compare first and last 4KB with a 64-bit xxHash boundary hash.
3. Stream full SHA-256 hashes through a pooled 64KB buffer for exact-match confirmation.

The GUI uses a Windows-native Go toolkit, so it does not require `gcc`, MSYS2, GTK, or CGO/OpenGL setup. It provides folder selection, live progress, grouped duplicate results, an Explorer-style visual preview pane, text previews for text-like files, keep-newest/keep-oldest selection, and trash-first deletion.

## Visual Design

The GUI uses a macOS-inspired, Liquid Glass-adjacent theme adapted to the Windows-native toolkit. It applies layered neutral surfaces, compact toolbar grouping, subdued secondary text, Apple-style accent colors, and cleaner preview cards while preserving native Windows accessibility, keyboard behavior, and file dialogs.

## Scan Workflow

The app uses a two-step workflow:

1. `Surface Scan` reads metadata for user-created files only. It skips system folders, application folders, dependency folders, executable/installable files, and protected OS locations.
2. After the surface scan, the `File Focus` switches become active. Select the file types to inspect, then click `Find Duplicates`.

Supported focus switches include PDF, Text, Word, Excel, PowerPoint, Images, Audio, Video, Archives, and Other. Each switch shows the number of eligible files found during the surface scan.

After a surface scan, the results table shows all eligible user-created files with `Scanned` status. If a duplicate scan finds no exact duplicates, the table remains populated with the selected focus files marked `No duplicate`, so users can still preview files, inspect metadata, and open file locations.

The scanner is intentionally conservative. It avoids files under locations such as Windows, Program Files, ProgramData, AppData, Recycle Bin, System Volume Information, `.git`, `node_modules`, cache folders, virtual environments, build folders, and executable or installer file types such as `.exe`, `.dll`, `.sys`, `.msi`, `.appx`, and `.msix`.

## Folder Selection

The `Select Folder` button uses the modern Windows folder picker through `IFileOpenDialog` in folder mode. It shows the normal Explorer-style folder browser with breadcrumb navigation, left navigation, search, recent locations, OneDrive, network locations, and the standard Windows access model. If the modern picker is unavailable on a machine, the app falls back to the legacy folder picker instead of blocking the scan workflow.

## Diagnostics And Crash Reports

Each app run writes a session log to:

```text
%LOCALAPPDATA%\DuplicateFileFinder\logs
```

The app also writes a `crash-*.txt` report if an unexpected panic occurs. Crash reports include the failure scope, panic message, Go runtime version, OS/architecture, executable path, working directory, session log path, command-line arguments, relevant workflow fields, and a stack trace. They do not dump environment variables or file contents.

Use the `Open Logs` button in the top toolbar to open the log folder in File Explorer. When reporting an incident, include the latest `session-*.log` and any matching `crash-*.txt` file from the same time.

## Preview Support

The preview pane now has two layers:

- Explorer-style thumbnail preview through Windows Shell thumbnail providers. This covers common images and can also cover PDFs, Office files, audio album art, and video frames when Windows has a thumbnail provider installed.
- Embedded Rich Preview through the local Windows browser control. PDF, Word, Excel, PowerPoint, audio, and video formats automatically open in Rich Preview when their row is selected. The `Rich Preview` button remains available to reload that mode manually. Rendering depends on Windows, Office/PDF/media handlers, and local browser support.
- App-generated fallback preview. If Windows does not return a thumbnail, the app now shows a clean file-type card instead of an error. PDF cards include page count and a short extracted text snippet when the document allows it. Office, audio, video, image, and unknown file types show type-specific metadata guidance.
- Multi-file comparison preview. Select multiple rows in the results table to show a responsive preview grid in the preview pane. The grid wraps as the app window is resized and uses thumbnails or fallback cards per file. To keep the UI readable, the pane shows the first 12 selected files and displays a limit message when more files are selected.

If a file type cannot be rendered visually, the app still shows exact-hash verification details and lets you open the file location with `Show In Explorer`.

## Windows Prerequisites

Install Go. No C compiler is required.

The package embeds a Windows Common Controls v6 manifest in `cmd/dupfind/rsrc_windows_amd64.syso`. This is required by the native GUI toolkit and prevents startup failures such as `TTM_ADDTOOL failed`.

Verify:

```powershell
go version
```

## Run From Source

```powershell
cd "C:\Users\vasan\OneDrive\Documents\duplicate-file-finder-go"
go run ./cmd/dupfind
```

## Build an EXE

```powershell
cd "C:\Users\vasan\OneDrive\Documents\duplicate-file-finder-go"
go build -o DuplicateFileFinder.exe ./cmd/dupfind
.\DuplicateFileFinder.exe
```

For a double-clickable app without a console window:

```powershell
go build -ldflags="-H windowsgui" -o DuplicateFileFinder.exe ./cmd/dupfind
```

## Test

```powershell
go test ./...
```

## Safety Notes

- The scanner skips symlinks to avoid directory loops and dead-link failures.
- Locked, unreadable, or permission-denied files are counted and ignored instead of crashing the scan.
- Files are never loaded fully into RAM during hashing.
- Deletion tries the OS trash/recycle bin first, then falls back to permanent delete only when requested by the GUI confirmation path.
