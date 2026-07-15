# ADR 0006: Distribute a per-user MSIX installer

- Status: Accepted
- Date: 2026-07-12

## Context

TwinTidy needs an installed Windows distribution alongside the required portable ZIP. The installed form must preserve the application's `asInvoker` posture, support both production architectures, carry the same reviewed and signed executable as the portable package, and provide predictable install, upgrade, and uninstall behavior without a custom privileged bootstrapper.

Installer generators with separate commercial terms would add a legal and operational dependency that is unnecessary for this package. Windows already provides the MSIX deployment model and the Windows SDK provides `MakeAppx.exe` for package construction. MSIX also makes package identity, publisher identity, architecture, and version explicit inputs to deployment.

An MSIX `Publisher` is not a marketing name. Windows requires it to match the signing certificate subject distinguished name. Using `Kayilan Inc` or another friendly label in that field when the certificate subject differs would produce a package that cannot be signed or installed under the intended identity.

## Decision

TwinTidy's installed distribution is a per-user MSIX package built with the Windows SDK `MakeAppx.exe`. The portable ZIP remains a supported baseline for users or managed environments that do not permit direct MSIX installation.

The package identity is:

- `Name`: `KayilanInc.TwinTidy`
- `Publisher`: the exact, complete subject distinguished name from the approved public code-signing certificate
- `PublisherDisplayName`: `Kayilan Inc`
- `ProcessorArchitecture`: `x64` for the Go `amd64` build and `arm64` for the Go `arm64` build
- application execution level: current user, with no elevation request

The release pipeline creates separate x64 and ARM64 packages. It packages only the already signed executable and its verified build receipt and signed provenance. `MakeAppx.exe` runs before the final package signature is applied. The executable and MSIX are both signed and timestamped in a protected release environment; pull-request workflows receive no signing authority. Packaging or signature verification fails if the executable signer subject and MSIX `Publisher` are not an exact match. The approved certificate SHA-256 is pinned separately as release configuration and verified before publication.

Semantic versions map to the four-part numeric MSIX version separately from PE:

- Windows forbids MSIX major zero, so SemVer `MAJOR` maps to MSIX `MAJOR + 1`;
- a stable `MAJOR.MINOR.PATCH` maps to `(MAJOR + 1).MINOR.PATCH.0`;
- a prerelease must end in a numeric sequence from 1 through 65535 and maps that sequence to the fourth field;
- for example, `0.1.0-beta.1` maps to `1.1.0.1` and `0.1.0-beta.2` maps to `1.1.0.2`;
- build metadata is not accepted, SemVer major must be at most 65534 for MSIX, and all other numeric fields must be at most 65535.

Windows permits an in-place update only when the package has the same identity and publisher and a numerically greater four-part package version. Because `1.1.0.0` sorts below `1.1.0.1`, a stable `0.1.0` cannot replace `0.1.0-beta.1` through an in-place MSIX update. The protected tag gate requires every new public tag to map higher than every existing TwinTidy release and rejects prerelease-label collisions such as `alpha.1` and `beta.1` under the same core.

Windows owns registration, Start menu integration, upgrade, and uninstall for the current user. TwinTidy does not add a service, scheduled task, machine-wide component, custom updater, or elevated uninstall program. Downgrade and architecture changes require uninstalling the current package and installing the intended signed package.

## Consequences

### Positive

- Installation and removal use the Windows package lifecycle and do not require administrator privileges.
- Package identity and publisher identity are cryptographically bound by Windows.
- x64 and ARM64 packages remain explicit, independently testable release artifacts.
- The installer uses Microsoft tooling already supplied by the Windows SDK, without adding a third-party packaging-tool EULA or fee to the project.
- The same signed executable, source receipt, license, notices, and provenance can be inspected in both portable and installed distributions.

### Negative

- Direct MSIX installation depends on Windows App Installer or an equivalent approved deployment mechanism and a certificate chain trusted by the target computer.
- The public code-signing certificate subject must be known before the manifest can be finalized, so publisher identity cannot be guessed or substituted.
- The four-part numeric version does not fully express Semantic Versioning precedence; in particular, same-core prerelease-to-stable promotion needs the release constraint described above.
- MSIX does not provide an automatic downgrade. Rollback requires uninstalling the newer package before installing the earlier signed package.
- Two architecture-specific installer artifacts must be built, signed, tested, and supported.

## Alternatives considered

- **MSI built with WiX:** not selected because it adds a separate packaging toolchain and license/EULA decision, and TwinTidy does not need machine-wide MSI capabilities.
- **Inno Setup or another custom installer:** not selected because it introduces third-party installer terms and a custom lifecycle when the Windows package model is sufficient.
- **Portable ZIP only:** retained as a supported fallback but rejected as the only distribution because it cannot provide OS-managed installation, upgrade, and uninstall.
- **Microsoft Store only:** rejected because it would remove the direct-download option and introduce store-account and submission dependencies.
- **Custom elevated bootstrapper:** rejected because it expands the privileged attack surface without a product requirement.

## Validation

- Build MSIX packages with a reviewed Windows SDK `MakeAppx.exe` for both x64 and ARM64.
- Inspect each manifest and confirm the fixed package name, exact certificate-subject publisher, expected architecture, four-part version, full-trust desktop entry point, and current-user execution posture.
- Verify the signed executable and signed MSIX against the pinned certificate subject and certificate SHA-256, including a trusted timestamp.
- On native x64 and native Windows ARM64, install as a standard user, launch the application repeatedly, apply a numerically higher same-identity update, uninstall, reinstall, and confirm the expected runtime version after each transition.
- Confirm uninstall removes the application registration, installed payload, and package-private virtualized diagnostics; separately exported support files remain outside package ownership.
- Prove that a mismatched publisher, unsigned package, wrong architecture, downgrade attempt, or tampered payload is rejected.
