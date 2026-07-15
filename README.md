# TwinTidy

TwinTidy is a local, Windows-native duplicate-file review tool being hardened toward safe cleanup. It proves that files are byte-for-byte identical and lets you review which copies could be kept. Destructive cleanup is intentionally disabled in the current pre-release build because Windows' documented Shell recycle API cannot keep the action bound to the verified file identity.

It is designed around one rule: finding a duplicate is useful, but avoiding data loss is more important.

## What it does

TwinTidy uses a staged scan so expensive work is reserved for plausible matches:

1. Surface-scan eligible user files and record current Windows file identities.
2. Group candidates by exact byte size.
3. Compare the first and last 4 KiB with xxHash.
4. Stream the remaining candidates through SHA-256 for exact-match proof.

The desktop workflow includes:

- native Windows folder selection and progress reporting
- PDF, text, Word, Excel, PowerPoint, image, audio, video, archive, and `Other` focus filters
- grouped duplicate results with keep-newest and keep-oldest helpers
- checkbox-only cleanup planning; highlighting a row never means “remove it”
- Windows Shell thumbnails, local rich previews, and metadata fallback cards
- fail-closed cleanup capability gating; the current build never changes files
- local session diagnostics with no telemetry

TwinTidy finds exact duplicates by content, not merely by name or extension. A supported file whose extension is not in a named focus category can still be considered under `Other`.

## Safety model

A scan result is only a point-in-time observation. Before any future recycle action, TwinTidy must reopen the target and a distinct unselected keeper and revalidate their Windows file identities, sizes, timestamps, protection state, and complete SHA-256 content.

The current production adapter stops before any destructive native call. Windows `IFileOperation` consumes a path-derived Shell item rather than the verified file handle, so another process could replace the path occupant after verification. Cleanup will remain unavailable until an identity-bound, reversible adapter satisfies [ADR 0005](docs/adr/0005-disable-path-based-recycle.md).

Any future cleanup must fail closed:

- every selected file must still belong to its scanned duplicate group
- at least one separately identified, verified keeper must remain
- changed, replaced, missing, hard-linked, reparse-backed, or alternate-stream files are not recycled
- native cancellation, ambiguous outcomes, and access failures are reported as failures
- success is shown only after Windows reports success and the original source object is no longer present at that path
- there is no automatic permanent-delete fallback

See [Security model](docs/SECURITY_MODEL.md), [Architecture](docs/ARCHITECTURE.md), and [ADR 0002](docs/adr/0002-verified-recycle-safety.md) for the full contract.

## Privacy

Scanning, hashing, previews, and cleanup planning run locally under the current Windows account. TwinTidy has no account, cloud service, analytics SDK, advertising, or automatic file upload. It does not require administrator privileges for ordinary use.

Diagnostics are stored under:

```text
%LOCALAPPDATA%\TwinTidy\logs
```

Logs remain on the computer unless the user explicitly shares them. Review diagnostic files before attaching them to a report because local filesystem context can be sensitive.

## Supported platform

Production targets are:

| Operating system | Architecture | Portable | Per-user install |
|---|---:|---|---|
| Windows | x64 / `amd64` | `TwinTidy-<version>-windows-amd64.zip` | `TwinTidy-<version>-windows-amd64.msix` |
| Windows | ARM64 | `TwinTidy-<version>-windows-arm64.zip` | `TwinTidy-<version>-windows-arm64.msix` |

Windows `386` is not supported. Release binaries use `CGO_ENABLED=0`; the shipped application does not need Python, GCC, MSYS2, GTK, Electron, a browser server, or a background service.

See [Install, update, and uninstall TwinTidy](docs/INSTALL.md) for architecture selection, checksum and signature verification, portable use, and MSIX lifecycle instructions.

The scanner intentionally uses CPU and storage concurrency rather than requiring a GPU. Exact duplicate detection is generally storage-bound; future similarity analysis can add optional acceleration without weakening deterministic CPU fallback.

## Build from source

Requirements:

- the Go patch version declared in `go.mod`
- Windows PowerShell 5.1 or PowerShell 7+
- Windows for native GUI execution

Run the quality gates:

```powershell
git clone https://github.com/Soyuz-Tec/duplicate-file-finder-go.git
Set-Location duplicate-file-finder-go
$env:CGO_ENABLED = "0"
go mod verify
go vet ./...
go test ./... -count=1
```

Run from source:

```powershell
go run ./cmd/twintidy
```

Build one architecture:

```powershell
./scripts/build.ps1 -Architecture amd64 -Version dev
```

Generate both architecture-specific Windows resource objects:

```powershell
./scripts/generate-resources.ps1 -Architecture all
```

Resource generation uses the pinned pure-Go `go-winres` tool. Generated `.syso` files contain the Common Controls v6/PerMonitorV2 manifest, TwinTidy icon, and PE version information.

## Release verification

Build exact-commit portable packages and run the source, receipt, reproducibility, and resource checks from a clean tree:

```powershell
./scripts/verify-release.ps1 -Version 0.1.0-beta.1
```

Direct development builds remain available from a dirty working tree and are labeled with their source digest. Official public artifacts require the signing, native x64/ARM64 smoke, MSIX lifecycle, checksum, receipt, and provenance gates described in [Release engineering](docs/RELEASE.md). An unsigned local build is a development artifact, not an official TwinTidy release.

## Development status

TwinTidy is being hardened toward its first reviewed release. The current pre-release build supports scanning, exact-match verification, selection planning, and previews; cleanup is disabled and no stable release may enable it without the identity-safety evidence required by ADR 0005.

Security or possible data-loss reports should be submitted through [GitHub private vulnerability reporting](https://github.com/Soyuz-Tec/duplicate-file-finder-go/security/advisories/new), not a public issue.

## License

TwinTidy is licensed under the [MIT License](LICENSE). Copyright (c) 2026 Kayilan Inc.
