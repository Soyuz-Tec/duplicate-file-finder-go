# TwinTidy signing adapter

TwinTidy keeps code-signing credentials outside the repository and outside the unsigned build job. The release scripts expose one provider-neutral adapter boundary so a local certificate, hardware token, managed signing service, or future provider can be used without weakening artifact verification.

## Trust boundary

The unsigned build remains the reproducible source artifact. `TwinTidy.build-receipt.json` binds its exact `TwinTidy.exe` hash and size to the Git source identity. Authenticode changes executable bytes, so the receipt must never be rewritten to describe a signed executable.

The signed release flow creates two additional records:

- `TwinTidy.signed-provenance.json` binds the immutable unsigned executable and build receipt to the signed executable, expected signer certificate, and timestamp certificate.
- `<package>.provenance.json` binds the final ZIP hash to its embedded signed executable, build receipt, and signed provenance.

The signed ZIP contains `LICENSE`, `THIRD_PARTY_NOTICES.txt`, the original build receipt, the signed executable, and the signed provenance. `SHA256SUMS.signed.txt` covers each ZIP and package-provenance sidecar.

## Adapter contract

`scripts/sign-release.ps1` invokes the configured adapter once per architecture:

```powershell
& <adapter> -InputPath <absolute-unsigned-exe> -OutputPath <absolute-signed-exe>
```

The adapter must:

1. Read but never modify, delete, or rename `InputPath`.
2. Create a new file at `OutputPath`; the path will not already exist.
3. Apply an Authenticode signature using SHA-256 and a public code-signing certificate.
4. Request a trusted timestamp from the configured signing provider.
5. Return exit code zero only after the provider reports success.
6. Keep credentials, tokens, PINs, certificate material, and service configuration out of stdout, stderr, repository files, and provenance.

The adapter may be a PowerShell script or an executable. It owns provider-specific authentication and command syntax. It must not publish artifacts or modify GitHub state.

The orchestrator rejects the adapter output unless all of these hold:

- the unsigned executable and build receipt still match their independently captured SHA-256 digests;
- the signed output differs from the unsigned executable;
- Windows reports Authenticode status `Valid`;
- the signer certificate subject exactly matches release configuration;
- the configured certificate-identity policy passes;
- a timestamp certificate is present;
- the generated provenance revalidates against all expected digests.

The verification policy has two modes:

- `fixed-certificate` pins the 64-character SHA-256 of the leaf certificate's raw DER bytes. `TWIN_TIDY_SIGNER_THUMBPRINT` carries that SHA-256 for compatibility with the script interface; it is not PowerShell's SHA-1 `X509Certificate2.Thumbprint`.
- `artifact-signing` pins the exact durable Artifact Signing subscriber identity EKU. The certificate must also contain Microsoft's Public Trust marker EKU `1.3.6.1.4.1.311.97.1.0`, the standard code-signing EKU, a trusted chain, and the configured exact subject. Each rotating leaf certificate SHA-256 is recorded in provenance but is not treated as durable identity.

These values are public certificate identifiers, not secrets. Update fixed-certificate or durable-EKU configuration through a reviewed identity-rotation change.

## Example adapter shape

This example is deliberately incomplete. Replace the provider command with the approved signing client, and configure credentials through that provider's protected identity mechanism.

```powershell
#requires -Version 5.1
param(
    [Parameter(Mandatory = $true)][string]$InputPath,
    [Parameter(Mandatory = $true)][string]$OutputPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

[System.IO.File]::Copy($InputPath, $OutputPath, $false)
& <provider-client> sign --file $OutputPath --digest sha256 --timestamp
if ($LASTEXITCODE -ne 0) {
    throw "Signing provider failed."
}
```

Do not add a real adapter containing organization-specific identifiers or credentials to the repository. A workflow may generate a short adapter at runtime or download a reviewed, pinned signing client.

## Release invocation

The build job must capture the hashes returned by `scripts/build.ps1`. Pass those values unchanged to the signing job:

```powershell
$signed = @(
    .\scripts\sign-release.ps1 `
        -Version 1.2.3-beta.1 `
        -Architecture all `
        -InputDirectory .\dist `
        -OutputDirectory .\dist\signed `
        -SourceDate $sourceDate `
        -ExpectedCommit $commit `
        -ExpectedGitTree $gitTree `
        -ExpectedSourceTreeSHA256 $sourceTreeSHA256 `
        -SignerAdapter $env:TWIN_TIDY_SIGNER_ADAPTER `
        -ExpectedSignerSubject $env:TWIN_TIDY_SIGNER_SUBJECT `
        -ExpectedSignerThumbprint $env:TWIN_TIDY_SIGNER_THUMBPRINT `
        -SignerVerificationMode $env:TWIN_TIDY_SIGNER_VERIFICATION_MODE `
        -ExpectedSignerIdentityEku $env:TWIN_TIDY_SIGNER_IDENTITY_EKU `
        -ExpectedUnsignedExecutableSHA256 $unsignedHashes `
        -ExpectedBuildReceiptSHA256 $receiptHashes
)
```

Capture `SignedExecutableSHA256`, `BuildReceiptSHA256`, and `SignedProvenanceSHA256` from those results, then package only those exact files:

```powershell
.\scripts\package-signed.ps1 `
    -Version 1.2.3-beta.1 `
    -Architecture all `
    -InputDirectory .\dist\signed `
    -OutputDirectory .\dist\signed-packages `
    -SourceDate $sourceDate `
    -ExpectedSignerSubject $env:TWIN_TIDY_SIGNER_SUBJECT `
    -ExpectedSignerThumbprint $env:TWIN_TIDY_SIGNER_THUMBPRINT `
    -SignerVerificationMode $env:TWIN_TIDY_SIGNER_VERIFICATION_MODE `
    -ExpectedSignerIdentityEku $env:TWIN_TIDY_SIGNER_IDENTITY_EKU `
    -ExpectedUnsignedExecutableSHA256 $unsignedHashes `
    -ExpectedBuildReceiptSHA256 $receiptHashes `
    -ExpectedSignedExecutableSHA256 $signedHashes `
    -ExpectedSignedProvenanceSHA256 $signedProvenanceHashes
```

Run `scripts/test-signed-release-hardening.ps1` in ordinary CI. It needs no certificate and verifies policy rejection, provenance tamper detection, unsigned-input immutability, and fail-closed handling of an unsigned adapter output. A protected release job must additionally run the real adapter and complete Authenticode verification before any GitHub release is created.

## Operational requirements

- Run signing only in a protected release environment, never for pull requests or untrusted forks.
- Grant the signing identity only the minimum operation needed to sign the selected release digest.
- Use short-lived workload identity when the provider supports it; otherwise protect and rotate credentials according to the provider's guidance.
- Retain signing-provider audit logs and correlate them with the Git commit, release tag, and published provenance.
- Treat any subject, certificate SHA-256, timestamp, digest, or provenance mismatch as a release-stopping failure.
- Never substitute a self-signed or test certificate for a public release.
