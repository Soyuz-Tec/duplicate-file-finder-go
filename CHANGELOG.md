# Changelog

All notable TwinTidy changes will be documented here. The project uses [Semantic Versioning](https://semver.org/) once versioned releases begin.

## [Unreleased]

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
