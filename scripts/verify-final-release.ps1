#requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][string]$Commit,
    [Parameter(Mandatory = $true)][string]$SourceDate,
    [Parameter(Mandatory = $true)][string]$Directory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "TwinTidy.Version.ps1")
. (Join-Path $PSScriptRoot "TwinTidy.Release.ps1")

$versionInfo = Resolve-TwinTidyVersion -Version $Version
$root = [System.IO.Path]::GetFullPath($Directory)
if (-not [System.IO.Directory]::Exists($root) -or $Commit -notmatch '^[0-9a-fA-F]{40,64}$') {
    throw "Final release directory or commit identity is invalid."
}
$Commit = $Commit.ToLowerInvariant()
$manifestPath = Join-Path $root "TwinTidy.release-manifest.json"
$checksumPath = Join-Path $root "SHA256SUMS.txt"
foreach ($required in @($manifestPath, $checksumPath)) {
    if (-not [System.IO.File]::Exists($required)) { throw "Final release control file is missing: $required" }
}

$expectedArtifactNames = @()
foreach ($arch in @("amd64", "arm64")) {
    $base = "TwinTidy-$($versionInfo.Canonical)-windows-$arch"
    $expectedArtifactNames += @("$base.zip", "$base.zip.provenance.json", "$base.msix", "$base.msix.provenance.json")
}
$expectedFileNames = @($expectedArtifactNames + @("SHA256SUMS.txt", "TwinTidy.release-manifest.json") | Sort-Object)
$actualFileNames = @(Get-ChildItem -LiteralPath $root -File | Select-Object -ExpandProperty Name | Sort-Object)
if (($actualFileNames -join "|") -cne ($expectedFileNames -join "|")) {
    throw "Final release has an unexpected file set: $($actualFileNames -join ', ')."
}

$manifest = ConvertFrom-TwinTidyJson -Json ([System.IO.File]::ReadAllText($manifestPath, [System.Text.UTF8Encoding]::new($false, $true)))
if ([string]$manifest.schema -cne "twintidy.release-manifest/v1" -or
    [string]$manifest.product -cne "TwinTidy" -or
    [string]$manifest.version -cne $versionInfo.Canonical -or
    [string]$manifest.sourceDate -cne $SourceDate -or
    [string]$manifest.source.commit -cne $Commit -or
    [string]$manifest.publisher.displayName -cne "Kayilan Inc" -or
    [string]::IsNullOrWhiteSpace([string]$manifest.publisher.certificateSubject)) {
    throw "Final release manifest identity is invalid."
}
$verificationMode = [string]$manifest.publisher.verificationMode
if ($verificationMode -notin @("fixed-certificate", "artifact-signing")) {
    throw "Final release manifest has invalid signer verification mode '$verificationMode'."
}
$certificateHashes = @($manifest.publisher.certificatesSHA256)
foreach ($certificateHash in $certificateHashes) {
    $null = Normalize-TwinTidyCertificateThumbprint -Thumbprint ([string]$certificateHash
    )
}
if ($verificationMode -ceq "fixed-certificate" -and $certificateHashes.Count -ne 1) {
    throw "Fixed-certificate releases must record exactly one leaf certificate SHA-256."
}
$expectedCertificateSHA256 = if ($verificationMode -ceq "fixed-certificate") { [string]$certificateHashes[0] } else { "" }
$expectedIdentityEku = if ($verificationMode -ceq "artifact-signing") { [string]$manifest.publisher.durableIdentityEku } else { "" }

$manifestArtifacts = @{}
foreach ($artifact in @($manifest.artifacts)) {
    $name = [string]$artifact.path
    if ($name -notin $expectedArtifactNames -or $manifestArtifacts.ContainsKey($name)) {
        throw "Release manifest contains unexpected or duplicate artifact '$name'."
    }
    $path = Join-Path $root $name
    $actualHash = Get-TwinTidyFileSHA256 -Path $path
    $actualSize = ([System.IO.FileInfo]::new($path)).Length
    if ([string]$artifact.sha256 -cne $actualHash -or [int64]$artifact.size -ne $actualSize) {
        throw "Release manifest binding failed for '$name'."
    }
    $manifestArtifacts[$name] = $actualHash
}
if ($manifestArtifacts.Count -ne $expectedArtifactNames.Count) {
    throw "Release manifest does not cover every downloadable artifact."
}

$checksumEntries = @{}
foreach ($line in [System.IO.File]::ReadAllLines($checksumPath, [System.Text.UTF8Encoding]::new($false, $true))) {
    if ($line -notmatch '^([0-9a-f]{64})  ([^\\/]+)$') {
        throw "Invalid SHA256SUMS.txt line '$line'."
    }
    $name = $Matches[2]
    if ($name -eq "SHA256SUMS.txt" -or $name -notin @($expectedArtifactNames + "TwinTidy.release-manifest.json") -or $checksumEntries.ContainsKey($name)) {
        throw "SHA256SUMS.txt contains unexpected, recursive, or duplicate entry '$name'."
    }
    $actualHash = Get-TwinTidyFileSHA256 -Path (Join-Path $root $name)
    if ($actualHash -cne $Matches[1]) { throw "Checksum mismatch for '$name'." }
    $checksumEntries[$name] = $actualHash
}
if ($checksumEntries.Count -ne ($expectedArtifactNames.Count + 1)) {
    throw "SHA256SUMS.txt does not cover every artifact and the release manifest."
}

function Read-ZipEntryBytes {
    param(
        [Parameter(Mandatory = $true)]$Entry,
        [Parameter(Mandatory = $true)][string]$Description
    )
    if ($Entry.Length -gt 2MB) { throw "$Description is too large." }
    $stream = $Entry.Open()
    try {
        $memory = [System.IO.MemoryStream]::new()
        try {
            $stream.CopyTo($memory)
            return $memory.ToArray()
        } finally {
            $memory.Dispose()
        }
    } finally {
        $stream.Dispose()
    }
}

Add-Type -AssemblyName System.IO.Compression.FileSystem
$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("TwinTidyFinalVerify-" + [System.Guid]::NewGuid().ToString("N"))
[System.IO.Directory]::CreateDirectory($tempRoot) | Out-Null
try {
    foreach ($arch in @("amd64", "arm64")) {
        $base = "TwinTidy-$($versionInfo.Canonical)-windows-$arch"
        $zipPath = Join-Path $root "$base.zip"
        $packageProvenancePath = "$zipPath.provenance.json"
        $packageProvenance = ConvertFrom-TwinTidyJson -Json ([System.IO.File]::ReadAllText($packageProvenancePath, [System.Text.UTF8Encoding]::new($false, $true)))
        if ([string]$packageProvenance.schema -cne "twintidy.signed-package-provenance/v1" -or
            [string]$packageProvenance.version -cne $versionInfo.Canonical -or
            [string]$packageProvenance.architecture -cne $arch -or
            [string]$packageProvenance.sourceDate -cne $SourceDate -or
            [string]$packageProvenance.source.commit -cne $Commit -or
            [string]$packageProvenance.package.path -cne "$base.zip" -or
            [string]$packageProvenance.package.sha256 -cne (Get-TwinTidyFileSHA256 -Path $zipPath) -or
            [int64]$packageProvenance.package.size -ne ([System.IO.FileInfo]::new($zipPath)).Length) {
            throw "Portable package provenance identity is invalid for $arch."
        }

        $archive = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
        try {
            $entries = @{}
            foreach ($entry in $archive.Entries) {
                $name = $entry.FullName.Replace('\', '/')
                if ($entries.ContainsKey($name) -or $name -notin @("LICENSE", "THIRD_PARTY_NOTICES.txt", "TwinTidy.build-receipt.json", "TwinTidy.exe", "TwinTidy.signed-provenance.json")) {
                    throw "Portable package contains unexpected or duplicate entry '$name'."
                }
                $entries[$name] = $entry
            }
            if ($entries.Count -ne 5) { throw "Portable package is missing required entries for $arch." }
            $receiptBytes = Read-ZipEntryBytes -Entry $entries["TwinTidy.build-receipt.json"] -Description "$arch build receipt"
            $provenanceBytes = Read-ZipEntryBytes -Entry $entries["TwinTidy.signed-provenance.json"] -Description "$arch signed provenance"
            $hashAlgorithm = [System.Security.Cryptography.SHA256]::Create()
            try {
                $receiptHash = ConvertTo-TwinTidyHex -Bytes $hashAlgorithm.ComputeHash($receiptBytes)
                $provenanceHash = ConvertTo-TwinTidyHex -Bytes $hashAlgorithm.ComputeHash($provenanceBytes)
            } finally {
                $hashAlgorithm.Dispose()
            }
            if ($receiptHash -cne [string]$packageProvenance.contents.buildReceiptSHA256 -or
                $provenanceHash -cne [string]$packageProvenance.contents.signedProvenanceSHA256) {
                throw "Portable receipt or provenance hash mismatch for $arch."
            }
            $receipt = ConvertFrom-TwinTidyJson -Json ([System.Text.UTF8Encoding]::new($false, $true).GetString($receiptBytes))
            $signedProvenance = ConvertFrom-TwinTidyJson -Json ([System.Text.UTF8Encoding]::new($false, $true).GetString($provenanceBytes))
            $receiptBinding = Assert-TwinTidyBuildReceipt `
                -Receipt $receipt `
                -ExpectedVersion $versionInfo.Canonical `
                -ExpectedArchitecture $arch `
                -ExpectedSourceDate $SourceDate `
                -ExpectedExecutableSHA256 ([string]$packageProvenance.contents.unsignedExecutableSHA256)
            if ($receiptBinding.SourceKind -cne "git-commit" -or -not $receiptBinding.SourceClean -or $receiptBinding.Commit -cne $Commit) {
                throw "Portable build receipt is not bound to the clean release commit for $arch."
            }
            $null = Assert-TwinTidySignedProvenance `
                -Provenance $signedProvenance `
                -ExpectedVersion $versionInfo.Canonical `
                -ExpectedArchitecture $arch `
                -ExpectedSourceDate $SourceDate `
                -ExpectedCommit $Commit `
                -ExpectedGitTree $receiptBinding.GitTree `
                -ExpectedSourceTreeSHA256 $receiptBinding.SourceTreeSHA256 `
                -ExpectedUnsignedExecutableSHA256 ([string]$packageProvenance.contents.unsignedExecutableSHA256) `
                -ExpectedBuildReceiptSHA256 $receiptHash `
                -ExpectedSignedExecutableSHA256 ([string]$packageProvenance.contents.signedExecutableSHA256) `
                -ExpectedSignerSubject ([string]$manifest.publisher.certificateSubject) `
                -ExpectedSignerThumbprint $expectedCertificateSHA256 `
                -SignerVerificationMode $verificationMode `
                -ExpectedSignerIdentityEku $expectedIdentityEku

            $executablePath = Join-Path $tempRoot "TwinTidy-$arch.exe"
            $executableStream = $entries["TwinTidy.exe"].Open()
            try {
                $output = [System.IO.File]::Open($executablePath, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
                try { $executableStream.CopyTo($output) } finally { $output.Dispose() }
            } finally { $executableStream.Dispose() }
            if ((Get-TwinTidyFileSHA256 -Path $executablePath) -cne [string]$packageProvenance.contents.signedExecutableSHA256) {
                throw "Portable signed executable hash mismatch for $arch."
            }
            $portableSignature = Assert-TwinTidyAuthenticodeSignature `
                -Path $executablePath `
                -ExpectedSignerSubject ([string]$manifest.publisher.certificateSubject) `
                -ExpectedSignerThumbprint $expectedCertificateSHA256 `
                -SignerVerificationMode $verificationMode `
                -ExpectedSignerIdentityEku $expectedIdentityEku `
                -RequireTimestamp $true
            if ($certificateHashes -cnotcontains $portableSignature.SignerThumbprint) {
                throw "Portable signer certificate SHA-256 is absent from the release manifest for $arch."
            }
        } finally {
            $archive.Dispose()
        }

        $msixPath = Join-Path $root "$base.msix"
        $msixProvenancePath = "$msixPath.provenance.json"
        $msixProvenance = ConvertFrom-TwinTidyJson -Json ([System.IO.File]::ReadAllText($msixProvenancePath, [System.Text.UTF8Encoding]::new($false, $true)))
        if ([string]$msixProvenance.schema -cne "twintidy.msix-provenance/v1" -or
            [string]$msixProvenance.version -cne $versionInfo.Canonical -or
            [string]$msixProvenance.architecture -cne $arch -or
            [string]$msixProvenance.sourceDate -cne $SourceDate -or
            [string]$msixProvenance.source.commit -cne $Commit -or
            [string]$msixProvenance.signedPackage.path -cne "$base.msix" -or
            [string]$msixProvenance.signedPackage.sha256 -cne (Get-TwinTidyFileSHA256 -Path $msixPath) -or
            [int64]$msixProvenance.signedPackage.size -ne ([System.IO.FileInfo]::new($msixPath)).Length) {
            throw "MSIX provenance identity is invalid for $arch."
        }
        $msixSignature = Assert-TwinTidyAuthenticodeSignature `
            -Path $msixPath `
            -ExpectedSignerSubject ([string]$manifest.publisher.certificateSubject) `
            -ExpectedSignerThumbprint $expectedCertificateSHA256 `
            -SignerVerificationMode $verificationMode `
            -ExpectedSignerIdentityEku $expectedIdentityEku `
            -RequireTimestamp $true
        if ($certificateHashes -cnotcontains $msixSignature.SignerThumbprint) {
            throw "MSIX signer certificate SHA-256 is absent from the release manifest for $arch."
        }
        $null = Assert-TwinTidyMSIXPackageBinding `
            -Path $msixPath `
            -ExpectedPackageName "KayilanInc.TwinTidy" `
            -ExpectedPublisher ([string]$manifest.publisher.certificateSubject) `
            -ExpectedPackageVersion $versionInfo.MSIXVersion `
            -ExpectedArchitecture $(if ($arch -ceq "amd64") { "x64" } else { "arm64" }) `
            -ExpectedExecutableSHA256 ([string]$msixProvenance.executable.sha256) `
            -ExpectedBuildReceiptSHA256 ([string]$msixProvenance.buildReceipt.sha256) `
            -ExpectedSignedProvenanceSHA256 ([string]$msixProvenance.signedProvenance.sha256) `
            -RequirePackageSignature
    }
} finally {
    $fullTemp = [System.IO.Path]::GetFullPath($tempRoot)
    $systemTemp = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    if (-not $systemTemp.EndsWith([System.IO.Path]::DirectorySeparatorChar.ToString())) { $systemTemp += [System.IO.Path]::DirectorySeparatorChar }
    if (-not $fullTemp.StartsWith($systemTemp, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not [System.IO.Path]::GetFileName($fullTemp).StartsWith("TwinTidyFinalVerify-", [System.StringComparison]::Ordinal)) {
        throw "Refusing to remove unexpected final-verification directory: $fullTemp"
    }
    if ([System.IO.Directory]::Exists($fullTemp)) { [System.IO.Directory]::Delete($fullTemp, $true) }
}

[pscustomobject]@{
    Version = $versionInfo.Canonical
    Commit = $Commit
    ArtifactCount = $expectedArtifactNames.Count
    ChecksumsVerified = $true
    ProvenanceVerified = $true
    SignaturesVerified = $true
    MSIXBindingsVerified = $true
}
