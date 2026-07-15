# Install, update, and uninstall TwinTidy

TwinTidy supports Windows x64 and Windows ARM64. Official releases provide a portable ZIP and a per-user MSIX for each architecture. Download artifacts only from the repository's GitHub Releases page.

## Safety limitation in this prerelease

TwinTidy `v0.1.0-beta.1` finds and reviews byte-for-byte duplicate files. Cleanup is intentionally disabled: the application does not recycle, delete, rename, or otherwise change a scanned file. Selecting potential cleanup rows is a review and planning action only; it does not reclaim disk space.

This limitation is deliberate. Windows' documented path-based Recycle Bin API cannot keep the destructive action bound to the file identity that TwinTidy verified. Cleanup will remain disabled until an identity-bound, reversible implementation satisfies the safety requirements in [ADR 0005](adr/0005-disable-path-based-recycle.md).

## Choose the correct download

Check **Settings > System > About > System type**:

| System type | Portable package | Installed package |
|---|---|---|
| x64-based processor | `TwinTidy-0.1.0-beta.1-windows-amd64.zip` | `TwinTidy-0.1.0-beta.1-windows-amd64.msix` |
| ARM-based processor | `TwinTidy-0.1.0-beta.1-windows-arm64.zip` | `TwinTidy-0.1.0-beta.1-windows-arm64.msix` |

Do not install an artifact whose name contains `.unsigned`. Unsigned files are release-pipeline intermediates, not public TwinTidy releases.

## Verify a download

An official release publishes `SHA256SUMS.txt` beside the downloadable artifacts. A checksum detects an incomplete or altered download. A valid digital signature with the certificate subject and thumbprint recorded on that GitHub release establishes the publisher identity. Check both before running the program.

Calculate an artifact's SHA-256 value in PowerShell:

```powershell
Get-FileHash .\TwinTidy-0.1.0-beta.1-windows-amd64.zip -Algorithm SHA256
Get-Content .\SHA256SUMS.txt
```

The 64-character hash must exactly match the line for that filename. Use the MSIX filename in the first command when verifying an installer.

For a portable package, extract the ZIP and verify the executable signature:

```powershell
$signature = Get-AuthenticodeSignature .\TwinTidy.exe
$algorithm = [System.Security.Cryptography.SHA256]::Create()
try {
    $certificateSHA256 = ([System.BitConverter]::ToString(
        $algorithm.ComputeHash($signature.SignerCertificate.RawData)
    )).Replace("-", "")
} finally {
    $algorithm.Dispose()
}
$signature | Format-List Status, StatusMessage
$signature.SignerCertificate.Subject
$certificateSHA256
```

`Status` must be `Valid`. The complete `Subject` and certificate SHA-256 must match the values on the GitHub release; the display name `Kayilan Inc` alone is not sufficient verification. PowerShell's built-in certificate `Thumbprint` property is normally SHA-1, so do not compare it with the release's stronger SHA-256 value.

For an MSIX, Windows App Installer must show the expected verified publisher and accept the package signature. If the Windows SDK is installed, the definitive command-line check is:

```powershell
signtool verify /pa /all /v .\TwinTidy-0.1.0-beta.1-windows-amd64.msix
```

Do not bypass a certificate warning, import an unverified certificate, or enable a policy that accepts unsigned packages. If signature verification fails, delete the download and report the release through the project's private security-reporting channel.

## Portable ZIP

### Install and run

1. Verify the ZIP checksum.
2. Extract the entire archive to a new directory under your user profile, such as `%LOCALAPPDATA%\Programs\TwinTidy\0.1.0-beta.1`.
3. Verify the extracted `TwinTidy.exe` signature and publisher certificate.
4. Run `TwinTidy.exe`. TwinTidy does not require administrator privileges.

Keep `LICENSE`, `THIRD_PARTY_NOTICES.txt`, the build receipt, and signed provenance with the executable. Running directly from inside the ZIP is not supported.

### Update

1. Close TwinTidy.
2. Download and verify the new portable package for the same architecture.
3. Extract it to a new versioned directory and launch it there.
4. After confirming the new version works, remove the old application directory if it is no longer needed.

Portable packages do not update themselves. Keeping versions in separate directories makes rollback possible without overwriting a previously verified executable.

### Uninstall

Close TwinTidy and delete the directory where the portable package was extracted. Local diagnostic logs are stored separately under `%LOCALAPPDATA%\TwinTidy\logs`; remove that directory only if you also want to delete retained diagnostics.

## Per-user MSIX

### Install and run

1. Verify the MSIX checksum and expected publisher.
2. Double-click the architecture-matched `.msix` file.
3. Confirm that Windows App Installer shows the publisher recorded on the GitHub release, then select **Install**.
4. Open TwinTidy from the Start menu.

The package installs for the current user. It does not install a service or request administrator privileges. If organizational policy blocks direct MSIX installation, use the verified portable ZIP or ask the device administrator for an approved deployment; do not weaken signature policy.

PowerShell can install the same verified package for the current user:

```powershell
Add-AppxPackage -Path .\TwinTidy-0.1.0-beta.1-windows-amd64.msix
```

### Update

Download and verify a newer MSIX for the same architecture, then open it with App Installer. Windows replaces the installed payload only when the package name and certificate publisher match and the four-part package version is greater.

`0.1.0-beta.1` has package version `1.1.0.1`: MSIX adds one to the SemVer major because Windows forbids package major zero. A stable `0.1.0` would map to `1.1.0.0` and therefore cannot update it in place. The protected tag gate rejects that non-increasing transition; a later release must use a greater package version. Switching between x64 and ARM64 also requires uninstalling the current package.

### Uninstall or roll back

Open **Settings > Apps > Installed apps**, find **TwinTidy**, open its menu, and select **Uninstall**. The equivalent PowerShell command is:

```powershell
Get-AppxPackage -Name KayilanInc.TwinTidy | Remove-AppxPackage
```

MSIX does not perform an automatic downgrade. To roll back, uninstall the current package and then verify and install the earlier signed MSIX. Uninstalling removes the registered application, package payload, and package-private virtualized application data, including MSIX diagnostic logs. Export any diagnostics needed for support before uninstalling. This differs from the portable edition, whose `%LOCALAPPDATA%\TwinTidy\logs` directory remains until you remove it.

## Confirm the installed version

For a portable installation, run:

```powershell
.\TwinTidy.exe --version
```

For an MSIX installation, start TwinTidy and open **About**. The displayed semantic version, source commit, and source date must match the GitHub release. If they do not, uninstall or remove that copy and report the mismatch.
