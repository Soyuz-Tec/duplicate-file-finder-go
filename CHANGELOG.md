# Changelog

All notable TwinTidy changes will be documented here. The project uses [Semantic Versioning](https://semver.org/) once versioned releases begin.

## [Unreleased]

### Added

- Staticcheck, workflow linting, race-detector tests, and a total-coverage floor as CI quality gates.
- Fuzz targets for directory protection, user-file classification, and category mapping, plus staged-scan throughput benchmarks.
- Persisted interface preferences: the main window reopens at its last on-screen position and the folder picker starts from the previously scanned folder. Preferences live in `%LOCALAPPDATA%\TwinTidy\settings.json`, load fail-open, and are saved atomically.
- An Export Report action that saves the reviewed duplicate groups as CSV or JSON, including per-group hashes, per-file metadata, and a keep-one-copy reclaimable-bytes estimate. CSV cells that spreadsheets would evaluate as formulas are neutralized.
- ADR 0008 (Proposed) and a read-only retained-handle verification primitive with a disposable-fixture spike proving file-ID stability and path-swap detection through a move. This is research toward re-enabling cleanup; production recycling remains disabled under ADR 0005 and requires product-owner approval and a security review before implementation.

### Changed

- Replaced deprecated `syscall.Syscall` COM calls with `syscall.SyscallN` in the folder dialog and Shell thumbnail adapters, and removed an unused snapshot-verification helper superseded by its scope-aware variant.
- Report export now streams on a cancellable background worker instead of buffering the complete report on the UI thread.

### Security

- Report overwrite approval is bound to the exact format-normalized destination. Writes use short same-directory staging files, atomic publication, and cleanup on failure or cancellation; privacy guidance now calls out path/hash disclosure and independently configured sync, cloud, and network providers.

## [0.1.0-beta.1] - 2026-07-12

### Added

- TwinTidy product identity, native icon, build information, architecture records, security model, and release guardrails.
- MIT project license and Kayilan Inc publisher identity.
- Windows amd64 and ARM64 resource/build/package targets.
- Protected Authenticode signing and signed-artifact provenance boundaries.
- Per-user x64 and ARM64 MSIX packages with disposable-certificate lifecycle tests.
- Stable Windows file identities, hard-link/alternate-stream protection, reparse-safe traversal, and cancellable hashing.
- Deterministic GUI operation generations and checkbox-only cleanup intent.

### Changed

- Renamed the module, command, executable, diagnostics directory, and user-facing application from Duplicate File Finder Go to TwinTidy.
- Pinned the supported Go patch toolchain and made startup/smoke failures visible through process exit status.
- Drained the Walk message loop during UI smoke shutdown to remove a startup-layout race.

### Fixed

- Derived the MSIX logo assets with integer-only box filtering and a fixed PNG serialization so the deterministic asset check reproduces identical bytes on amd64 and ARM64 hosts; the previous GDI+ scaling was architecture-dependent.
- Kept MakeAppx console output out of the structured `package-msix.ps1` results, which crashed strict-mode consumers such as the MSIX lifecycle gate.

### Security

- Added group-aware pre-action revalidation and disabled path-based Windows Recycle Bin calls until the verified file identity can remain authoritative through the native operation.
