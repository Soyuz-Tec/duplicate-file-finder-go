# TwinTidy Release Process

Status: required release procedure, 2026-07-10

## Supported artifacts

TwinTidy ships separate Windows artifacts for:

- `windows-amd64`
- `windows-arm64`

Windows `386` is unsupported. Release binaries use `CGO_ENABLED=0` and run as the current user. Each architecture ships as both a portable ZIP and a per-user MSIX. The MSIX is publishable only after its native install, upgrade, uninstall, and reinstall lifecycle passes.

## Version policy

Use Semantic Versioning tags such as `v0.1.0-beta.1` and `v1.0.0`. The same version must appear in:

- the Git tag and GitHub release
- runtime `--version` and About information
- PE ProductVersion/FileVersion metadata
- artifact names
- installer metadata
- changelog entry

PE fixed versions are four numeric fields. Stable `MAJOR.MINOR.PATCH` maps to `MAJOR.MINOR.PATCH.0`. A prerelease must end in a numeric sequence from 1 through 65535, so `0.1.0-beta.1` maps to PE `0.1.0.1` while retaining the SemVer string in PE metadata and runtime output. Each numeric component must fit an unsigned 16-bit field. The special development version `dev` maps to PE `0.0.0.0`.

MSIX forbids a zero major version, so its distinct monotonic mapping is `(MAJOR + 1).MINOR.PATCH.SEQUENCE`; `0.1.0-beta.1` therefore maps to MSIX `1.1.0.1`. SemVer major `65535` is not MSIX-representable. Tag validation rejects any MSIX version collision, non-increasing package version, or reuse of one numeric prerelease sequence under different labels.

## Authority and prerequisites

A release must start from a clean, reviewed commit on the protected default branch. Required checks must pass before creating the protected tag. The release operator must have access to the protected GitHub release environment, signing service or certificate, and RFC 3161 timestamp service. A cleanup-enabled release is additionally blocked until ADR 0005 is superseded by a validated identity-bound adapter.

Never expose private keys, certificate passwords, signing tokens, or raw credentials in repository files, command history, logs, caches, or workflow artifacts.

## Source validation

From the repository root:

```powershell
$ErrorActionPreference = "Stop"

$goFiles = git ls-files "*.go"
$unformatted = gofmt -l $goFiles
if ($unformatted) { throw "Unformatted Go files:`n$($unformatted -join "`n")" }

go mod verify
$tidyDiff = @(go mod tidy -diff 2>&1)
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
if ($tidyDiff.Count -gt 0) {
    $tidyDiff | ForEach-Object { Write-Host $_ }
    throw "go.mod or go.sum is not tidy."
}
go vet ./...
go test ./... -count=1
git diff --check

if (git status --porcelain) { throw "Release tree is not clean." }
```

`verify-release.ps1` repeats this as a fail-closed gate before invoking any build step. Modified, staged, deleted, renamed, or untracked repository files are rejected. The complete destructive fault-injection suite, CodeQL, dependency checks, and native architecture jobs must also pass in GitHub Actions.

## Resource validation

The architecture-specific resources are generated from the checked-in manifest, icon, and version-resource configuration. Regenerate them through the repository script and require no unexplained diff:

```powershell
.\scripts\generate-resources.ps1
.\scripts\generate-resources.ps1 -Check
git diff --exit-code -- cmd\twintidy
```

The script runs pinned pure-Go `github.com/tc-hib/go-winres@v0.3.3`; it does not require Python, CGO, the Windows SDK, or a global tool installation. `-Check` generates each architecture twice, proves the bytes are deterministic, and compares them with the checked-in objects.

Each artifact must contain Common Controls v6, `asInvoker`, PerMonitorV2 DPI awareness, long-path awareness, the TwinTidy icon, and matching version fields. Source and extracted manifests are parsed as XML, not searched as text: exactly one active `requestedExecutionLevel` must exist in the Windows `trustInfo` path with `level="asInvoker"` and `uiAccess="false"`. Comments, duplicate elements, wrong namespaces, DTDs, and elevated active settings fail closed. The release is blocked if either `rsrc_windows_amd64.syso` or `rsrc_windows_arm64.syso` is absent.

The checked-in objects carry deterministic `dev` metadata. `build.ps1` copies the current source into a verified temporary staging directory, generates requested-version resources only in that stage, builds, and removes the stage. It verifies the checked-in development objects first and never leaves a version-specific resource in the working tree.

The approved product publisher is **Kayilan Inc** and the project is Copyright (c) 2026 Kayilan Inc. PE, installer, signature, and release metadata must use that identity consistently. A public production release remains blocked until the signing certificate's exact subject and certificate SHA-256 are approved and configured in the protected signing environment.

## Reproducible unsigned build

Use the pinned Go patch version from `go.mod`. Do not call `go env -w`. The canonical build script restores the caller's process environment after building both architectures.

```powershell
$Version = "0.1.0-beta.1"
$Commit = (git rev-parse HEAD).Trim()
$SourceDate = (git show -s --format=%cI HEAD).Trim()

$BuildResults = @(.\scripts\build.ps1 `
    -Version $Version `
    -Commit $Commit `
    -SourceDate $SourceDate `
    -SourceMode GitCommit)

$ExecutableHashes = @{}
$ReceiptHashes = @{}
foreach ($Result in $BuildResults) {
    $ExecutableHashes[$Result.Architecture] = $Result.SHA256
    $ReceiptHashes[$Result.Architecture] = $Result.ReceiptSHA256
}

.\scripts\package.ps1 `
    -Version $Version `
    -SourceDate $SourceDate `
    -ExpectedExecutableSHA256 $ExecutableHashes `
    -ExpectedBuildReceiptSHA256 $ReceiptHashes
```

`-SourceMode GitCommit` exports and builds the exact resolved commit rather than copying mutable working-tree files. The script requires the claimed source date to equal that commit's date and emits `TwinTidy.build-receipt.json` beside every executable. The machine-readable receipt binds the full commit, Git tree, canonical SHA-256 source-tree digest, architecture, resource digest, toolchain, and executable SHA-256. A normal `build.ps1` call still uses `-SourceMode WorkingTree` by default for practical local development; a dirty build is explicitly labeled with a `+working-tree.<digest>` runtime commit and is not release evidence.

`package.ps1` requires the executable and receipt hashes captured from the completed build. It locks both inputs against writes and replacement while creating the archive, embeds the receipt beside `TwinTidy.exe`, re-hashes both ZIP entries, and verifies the final archive/checksum binding. It creates deterministic portable ZIPs with normalized entry timestamps and a UTF-8 `SHA256SUMS.txt`. Build the unsigned executables and packages twice from separate absolute paths with the same commit, toolchain, resource inputs, and source-date metadata. Their SHA-256 values and receipts must match. Preserve those hashes and build provenance before signing.

Each portable ZIP contains `TwinTidy.exe`, its exact `TwinTidy.build-receipt.json`, the MIT `LICENSE`, and the deterministic `THIRD_PARTY_NOTICES.txt`. Validate the notice source before packaging:

```powershell
.\scripts\generate-notices.ps1 -Check
```

The repository verification command first rejects a dirty repository, then performs two independent exact-commit staged builds, verifies their source/build receipts, packages digest-pinned inputs, compares hashes, checks PE machine values, semantically validates extracted manifests, inspects icon and version resources, and proves that version-specific builds did not modify tracked development resources. `-VerifiedOutputDirectory` exports one immutable unsigned executable and build receipt per architecture plus `TwinTidy.unsigned-reproducibility.json` for the protected signing job:

```powershell
.\scripts\verify-release.ps1 -Version $Version
```

Run the fast adversarial release-script regression suite during development:

```powershell
.\scripts\test-release-hardening.ps1
```

It covers safe-looking manifest comments with an unsafe active policy, duplicate manifest-policy elements, dirty or untracked source rejection, a valid digest-bound package, and replacement of an executable after its trusted digest was captured.

Runtime `--version` output and PE numeric/string versions are compared with the requested version wherever the host can execute the target. On a native target host, add `-RunNativeSmoke`. The script explicitly reports a cross-architecture runtime or smoke check as skipped; the corresponding native CI job remains a production gate.

## Native smoke and safety matrix

Run the amd64 executable on native x64 Windows and the ARM64 executable on native Windows ARM64. A compile-only result is insufficient. Verify:

- process startup and clean exit status
- actual Walk window creation and Common Controls behavior
- high-DPI rendering and accessible keyboard focus
- folder selection and cancellation
- a read-only duplicate scan fixture
- a fail-closed unsupported-cleanup fixture; after an identity-bound adapter is approved, verified recycle success, cancellation, path-swap, and failure fixtures
- diagnostics path and privacy content

Production acceptance also requires the adversarial cases in `docs/SECURITY_MODEL.md`.

## Signing and packaging

Signing changes executable bytes and is intentionally performed after reproducibility verification:

```text
reproducible unsigned executable
-> record unsigned hash and provenance
-> Authenticode sign and timestamp executable
-> verify executable signature
-> package installer/archive
-> sign and timestamp installer
-> verify installer signature
-> calculate checksums of final distributed bytes
```

Example verification:

```powershell
Get-AuthenticodeSignature .\dist\TwinTidy.exe | Format-List
signtool verify /pa /all /v .\dist\TwinTidy.exe
signtool verify /pa /all /v .\dist\TwinTidy.msix
```

`sign-release.ps1` invokes the reviewed provider adapter without passing credentials on the command line, requires the exact configured signer subject and certificate SHA-256, requires a timestamp certificate, preserves the unsigned receipt, and emits `TwinTidy.signed-provenance.json`. `package-signed.ps1` creates the final portable archive only from those digest-pinned inputs. `package-msix.ps1` creates an unsigned package from the already signed executable; the protected job signs and re-verifies the MSIX before publication. See [the signing adapter contract](SIGNING_ADAPTER.md) and [ADR 0006](adr/0006-per-user-msix-distribution.md).

The final `SHA256SUMS.txt` uses SHA-256 and lists every downloadable artifact plus `TwinTidy.release-manifest.json`. Release notes identify the supported architectures, exact certificate identity, safety-impacting changes, known limitations, upgrade behavior, and rollback procedure.

## GitHub publication

1. Create an annotated tag only after required default-branch checks pass.
2. Let the protected release workflow rebuild from that tag; do not upload a developer-machine binary.
3. Publish initially as a draft or prerelease.
4. Verify signatures, checksums, provenance, archive contents, installer lifecycle, and native smoke evidence.
5. Promote the same immutable artifacts; do not replace assets under an existing version.

## Rollback and incident response

If a release can risk data loss or reports false deletion success:

1. Stop promotion and mark the GitHub release as affected or withdraw it without erasing evidence.
2. Disable update/download guidance pointing to the affected version.
3. Preserve logs, artifacts, provenance, source commit, and reproduction fixtures.
4. Publish a concise advisory with affected versions and safe user actions.
5. Fix through a reviewed patch release; never silently replace a signed artifact.
6. Update the security model, regression tests, and relevant ADR before resuming release.
