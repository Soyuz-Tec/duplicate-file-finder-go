#requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,

    [ValidateSet("all", "amd64", "arm64")]
    [string[]]$Architecture = @("all"),

    [string]$InputDirectory,

    [string]$OutputDirectory,

    [string]$SourceDate,

    [Parameter(Mandatory = $true)][string]$ExpectedSignerSubject,

    [string]$ExpectedSignerThumbprint,

    [ValidateSet("fixed-certificate", "artifact-signing")]
    [string]$SignerVerificationMode = "fixed-certificate",

    [string]$ExpectedSignerIdentityEku,

    [Parameter(Mandatory = $true)][hashtable]$ExpectedUnsignedExecutableSHA256,

    [Parameter(Mandatory = $true)][hashtable]$ExpectedBuildReceiptSHA256,

    [Parameter(Mandatory = $true)][hashtable]$ExpectedSignedExecutableSHA256,

    [Parameter(Mandatory = $true)][hashtable]$ExpectedSignedProvenanceSHA256
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "TwinTidy.Version.ps1")
. (Join-Path $PSScriptRoot "TwinTidy.Release.ps1")

$versionInfo = Resolve-TwinTidyVersion -Version $Version
if ($versionInfo.Canonical -ceq "dev") {
    throw "Development builds are not public signed-package inputs; provide a SemVer release version."
}
$repoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($InputDirectory)) { $InputDirectory = Join-Path (Join-Path $repoRoot "dist") "signed" }
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) { $OutputDirectory = Join-Path (Join-Path $repoRoot "dist") "signed-packages" }
$InputDirectory = [System.IO.Path]::GetFullPath($InputDirectory)
$OutputDirectory = [System.IO.Path]::GetFullPath($OutputDirectory)
if ([string]::IsNullOrWhiteSpace($ExpectedSignerSubject)) { throw "-ExpectedSignerSubject must be the exact certificate subject." }
if ($SignerVerificationMode -ceq "fixed-certificate") {
    $ExpectedSignerThumbprint = Normalize-TwinTidyCertificateThumbprint -Thumbprint $ExpectedSignerThumbprint
} elseif ([string]::IsNullOrWhiteSpace($ExpectedSignerIdentityEku)) {
    throw "Artifact Signing requires -ExpectedSignerIdentityEku."
}

if ($Architecture -contains "all") {
    if ($Architecture.Count -ne 1) { throw "Architecture 'all' cannot be combined with another architecture." }
    $targets = @("amd64", "arm64")
} else {
    $targets = @($Architecture)
}
if ([string]::IsNullOrWhiteSpace($SourceDate)) {
    Push-Location $repoRoot
    try {
        $SourceDate = (& git show -s --format=%cI HEAD 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($SourceDate)) { throw "Unable to resolve the source date." }
    } finally {
        Pop-Location
    }
}
$entryTime = [System.DateTimeOffset]::Parse($SourceDate).ToUniversalTime()
if ($entryTime -lt [System.DateTimeOffset]::new(1980, 1, 1, 0, 0, 0, [System.TimeSpan]::Zero)) {
    throw "ZIP timestamps cannot be earlier than 1980-01-01."
}

function Get-ExpectedDigest {
    param(
        [Parameter(Mandatory = $true)][hashtable]$Table,
        [Parameter(Mandatory = $true)][string]$TargetArchitecture,
        [Parameter(Mandatory = $true)][string]$Description
    )

    if (-not $Table.ContainsKey($TargetArchitecture)) { throw "$Description is missing an expected SHA-256 for $TargetArchitecture." }
    $digest = ([string]$Table[$TargetArchitecture]).ToLowerInvariant()
    if ($digest -notmatch '^[0-9a-f]{64}$') { throw "$Description for $TargetArchitecture is not a SHA-256 digest." }
    return $digest
}

function Read-LockedUtf8 {
    param(
        [Parameter(Mandatory = $true)][System.IO.Stream]$Stream,
        [Parameter(Mandatory = $true)][string]$Description
    )

    if (-not $Stream.CanSeek -or $Stream.Length -gt 1MB) { throw "$Description is too large or unreadable." }
    $bytes = [byte[]]::new([int]$Stream.Length)
    $Stream.Position = 0
    $offset = 0
    while ($offset -lt $bytes.Length) {
        $read = $Stream.Read($bytes, $offset, $bytes.Length - $offset)
        if ($read -eq 0) { throw "Unexpected end of $Description." }
        $offset += $read
    }
    $Stream.Position = 0
    return [System.Text.UTF8Encoding]::new($false, $true).GetString($bytes)
}

function Assert-SignedArchiveBinding {
    param(
        [Parameter(Mandatory = $true)][string]$ArchivePath,
        [Parameter(Mandatory = $true)][hashtable]$ExpectedEntries
    )

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [System.IO.Compression.ZipFile]::OpenRead($ArchivePath)
    try {
        $actualNames = @($archive.Entries | ForEach-Object { $_.FullName } | Sort-Object)
        $expectedNames = @($ExpectedEntries.Keys | Sort-Object)
        if (($actualNames -join "|") -cne ($expectedNames -join "|")) {
            throw "Unexpected signed package entries in '$ArchivePath': $($actualNames -join ', ')."
        }
        foreach ($entryName in $expectedNames) {
            $entry = $archive.GetEntry($entryName)
            $stream = $entry.Open()
            try {
                $actualHash = Get-TwinTidyStreamSHA256 -Stream $stream
            } finally {
                $stream.Dispose()
            }
            if ($actualHash -cne $ExpectedEntries[$entryName]) {
                throw "Signed package entry '$entryName' changed during packaging."
            }
        }
    } finally {
        $archive.Dispose()
    }
}

function Remove-SignedPackagingStagingDirectory {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$ExpectedParent
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    if ([System.IO.Path]::GetDirectoryName($fullPath) -cne [System.IO.Path]::GetFullPath($ExpectedParent) -or
        -not [System.IO.Path]::GetFileName($fullPath).StartsWith(".signed-packaging-", [System.StringComparison]::Ordinal)) {
        throw "Refusing to remove unexpected signed-package staging directory: $fullPath"
    }
    if ([System.IO.Directory]::Exists($fullPath)) {
        [System.IO.Directory]::Delete($fullPath, $true)
    }
}

$licensePath = Join-Path $repoRoot "LICENSE"
$noticePath = Join-Path $repoRoot "THIRD_PARTY_NOTICES.txt"
if (-not [System.IO.File]::Exists($licensePath)) { throw "License file is missing: $licensePath" }
if (-not [System.IO.File]::Exists($noticePath)) { throw "Third-party notice file is missing: $noticePath" }

[System.IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null
$finalChecksumPath = Join-Path $OutputDirectory "SHA256SUMS.signed.txt"
if ([System.IO.File]::Exists($finalChecksumPath)) { throw "Refusing to replace existing signed checksum file: $finalChecksumPath" }
foreach ($arch in $targets) {
    $baseName = "TwinTidy-$($versionInfo.Canonical)-windows-$arch.zip"
    foreach ($candidate in @((Join-Path $OutputDirectory $baseName), (Join-Path $OutputDirectory "$baseName.provenance.json"))) {
        if ([System.IO.File]::Exists($candidate)) { throw "Refusing to replace existing signed package output: $candidate" }
    }
}

$stagingRoot = Join-Path $OutputDirectory (".signed-packaging-" + [System.Guid]::NewGuid().ToString("N"))
[System.IO.Directory]::CreateDirectory($stagingRoot) | Out-Null
$checksumPath = Join-Path $stagingRoot "SHA256SUMS.signed.txt"
Add-Type -AssemblyName System.IO.Compression
$results = @()
$pendingResults = @()
$checksumArtifacts = @()
try {
foreach ($arch in $targets) {
    $unsignedHash = Get-ExpectedDigest -Table $ExpectedUnsignedExecutableSHA256 -TargetArchitecture $arch -Description "Expected unsigned executable digest"
    $receiptHash = Get-ExpectedDigest -Table $ExpectedBuildReceiptSHA256 -TargetArchitecture $arch -Description "Expected build-receipt digest"
    $signedHash = Get-ExpectedDigest -Table $ExpectedSignedExecutableSHA256 -TargetArchitecture $arch -Description "Expected signed executable digest"
    $signedProvenanceHash = Get-ExpectedDigest -Table $ExpectedSignedProvenanceSHA256 -TargetArchitecture $arch -Description "Expected signed-provenance digest"

    $artifactDirectory = Join-Path $InputDirectory "TwinTidy-$($versionInfo.Canonical)-windows-$arch"
    $signedPath = Join-Path $artifactDirectory "TwinTidy.exe"
    $receiptPath = Join-Path $artifactDirectory "TwinTidy.build-receipt.json"
    $signedProvenancePath = Join-Path $artifactDirectory "TwinTidy.signed-provenance.json"
    foreach ($path in @($signedPath, $receiptPath, $signedProvenancePath)) {
        if (-not [System.IO.File]::Exists($path)) { throw "Signed-package input is missing: $path" }
        if (([System.IO.FileInfo]::new($path).Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Signed-package input must not be a reparse point: $path"
        }
    }

    $receiptStream = [System.IO.File]::Open($receiptPath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
    try {
        if ((Get-TwinTidyStreamSHA256 -Stream $receiptStream) -cne $receiptHash) { throw "Build receipt digest does not match expected for $arch." }
        try { $receipt = ConvertFrom-TwinTidyJson -Json (Read-LockedUtf8 -Stream $receiptStream -Description "$arch build receipt") }
        catch { throw "Build receipt for $arch is invalid JSON: $($_.Exception.Message)" }
        $receiptBinding = Assert-TwinTidyBuildReceipt `
            -Receipt $receipt `
            -ExpectedVersion $versionInfo.Canonical `
            -ExpectedArchitecture $arch `
            -ExpectedSourceDate $SourceDate `
            -ExpectedExecutableSHA256 $unsignedHash

        $provenanceStream = [System.IO.File]::Open($signedProvenancePath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
        try {
            if ((Get-TwinTidyStreamSHA256 -Stream $provenanceStream) -cne $signedProvenanceHash) { throw "Signed provenance digest does not match expected for $arch." }
            try { $provenance = ConvertFrom-TwinTidyJson -Json (Read-LockedUtf8 -Stream $provenanceStream -Description "$arch signed provenance") }
            catch { throw "Signed provenance for $arch is invalid JSON: $($_.Exception.Message)" }
            $provenanceBinding = Assert-TwinTidySignedProvenance `
                -Provenance $provenance `
                -ExpectedVersion $versionInfo.Canonical `
                -ExpectedArchitecture $arch `
                -ExpectedSourceDate $SourceDate `
                -ExpectedCommit $receiptBinding.Commit `
                -ExpectedGitTree $receiptBinding.GitTree `
                -ExpectedSourceTreeSHA256 $receiptBinding.SourceTreeSHA256 `
                -ExpectedUnsignedExecutableSHA256 $unsignedHash `
                -ExpectedBuildReceiptSHA256 $receiptHash `
                -ExpectedSignedExecutableSHA256 $signedHash `
                -ExpectedSignerSubject $ExpectedSignerSubject `
                -ExpectedSignerThumbprint $ExpectedSignerThumbprint `
                -SignerVerificationMode $SignerVerificationMode `
                -ExpectedSignerIdentityEku $ExpectedSignerIdentityEku

            $signedStream = [System.IO.File]::Open($signedPath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
            try {
                if ((Get-TwinTidyStreamSHA256 -Stream $signedStream) -cne $signedHash) { throw "Signed executable digest does not match expected for $arch." }
                if ($signedStream.Length -ne $provenanceBinding.SignedExecutableSize) { throw "Signed executable size does not match signed provenance for $arch." }
                $signature = Assert-TwinTidyAuthenticodeSignature `
                    -Path $signedPath `
                    -ExpectedSignerSubject $ExpectedSignerSubject `
                    -ExpectedSignerThumbprint $ExpectedSignerThumbprint `
                    -SignerVerificationMode $SignerVerificationMode `
                    -ExpectedSignerIdentityEku $ExpectedSignerIdentityEku `
                    -RequireTimestamp $true

                $licenseStream = [System.IO.File]::Open($licensePath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
                try {
                    $noticeStream = [System.IO.File]::Open($noticePath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
                    try {
                        $archivePath = Join-Path $stagingRoot "TwinTidy-$($versionInfo.Canonical)-windows-$arch.zip"
                        $temporaryArchive = "$archivePath.tmp-$([System.Guid]::NewGuid().ToString('N'))"
                        $archiveCompleted = $false
                        try {
                            $archiveStream = [System.IO.File]::Open($temporaryArchive, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
                            try {
                                $archive = [System.IO.Compression.ZipArchive]::new($archiveStream, [System.IO.Compression.ZipArchiveMode]::Create, $false)
                                try {
                                    $packageFiles = @(
                                        [pscustomobject]@{ Entry = "LICENSE"; Stream = $licenseStream }
                                        [pscustomobject]@{ Entry = "THIRD_PARTY_NOTICES.txt"; Stream = $noticeStream }
                                        [pscustomobject]@{ Entry = "TwinTidy.build-receipt.json"; Stream = $receiptStream }
                                        [pscustomobject]@{ Entry = "TwinTidy.exe"; Stream = $signedStream }
                                        [pscustomobject]@{ Entry = "TwinTidy.signed-provenance.json"; Stream = $provenanceStream }
                                    ) | Sort-Object -Property Entry
                                    foreach ($packageFile in $packageFiles) {
                                        $packageFile.Stream.Position = 0
                                        $entry = $archive.CreateEntry($packageFile.Entry, [System.IO.Compression.CompressionLevel]::Optimal)
                                        $entry.LastWriteTime = $entryTime
                                        $entryStream = $entry.Open()
                                        try { $packageFile.Stream.CopyTo($entryStream) }
                                        finally { $entryStream.Dispose() }
                                    }
                                } finally {
                                    $archive.Dispose()
                                }
                            } finally {
                                $archiveStream.Dispose()
                            }
                            [System.IO.File]::Move($temporaryArchive, $archivePath)
                            $archiveCompleted = $true
                        } finally {
                            if (-not $archiveCompleted -and [System.IO.File]::Exists($temporaryArchive)) { [System.IO.File]::Delete($temporaryArchive) }
                        }
                    } finally {
                        $noticeStream.Dispose()
                    }
                } finally {
                    $licenseStream.Dispose()
                }
            } finally {
                $signedStream.Dispose()
            }
        } finally {
            $provenanceStream.Dispose()
        }
    } finally {
        $receiptStream.Dispose()
    }

    $entryDigests = @{
        "LICENSE" = Get-TwinTidyFileSHA256 -Path $licensePath
        "THIRD_PARTY_NOTICES.txt" = Get-TwinTidyFileSHA256 -Path $noticePath
        "TwinTidy.build-receipt.json" = $receiptHash
        "TwinTidy.exe" = $signedHash
        "TwinTidy.signed-provenance.json" = $signedProvenanceHash
    }
    Assert-SignedArchiveBinding -ArchivePath $archivePath -ExpectedEntries $entryDigests
    $archiveHash = Get-TwinTidyFileSHA256 -Path $archivePath
    $archiveInfo = [System.IO.FileInfo]::new($archivePath)
    $packageProvenance = [ordered]@{
        schema = "twintidy.signed-package-provenance/v1"
        product = "TwinTidy"
        version = $versionInfo.Canonical
        architecture = $arch
        sourceDate = $SourceDate
        generatedAt = [System.DateTimeOffset]::UtcNow.ToString("o")
        package = [ordered]@{
            path = $archiveInfo.Name
            sha256 = $archiveHash
            size = [int64]$archiveInfo.Length
        }
        contents = [ordered]@{
            unsignedExecutableSHA256 = $unsignedHash
            buildReceiptSHA256 = $receiptHash
            signedExecutableSHA256 = $signedHash
            signedProvenanceSHA256 = $signedProvenanceHash
        }
        authenticode = [ordered]@{
            signerSubject = $signature.SignerSubject
            signerThumbprint = $signature.SignerThumbprint
            verificationMode = $signature.VerificationMode
            signerIdentityEku = $signature.SignerIdentityEku
            timestampSubject = $signature.TimestampSubject
            timestampThumbprint = $signature.TimestampThumbprint
        }
        source = [ordered]@{
            commit = $receiptBinding.Commit
            gitTree = $receiptBinding.GitTree
            treeSHA256 = $receiptBinding.SourceTreeSHA256
        }
    }
    $packageProvenancePath = "$archivePath.provenance.json"
    [System.IO.File]::WriteAllText(
        $packageProvenancePath,
        (($packageProvenance | ConvertTo-Json -Depth 8) + "`n"),
        [System.Text.UTF8Encoding]::new($false)
    )
    $packageProvenanceHash = Get-TwinTidyFileSHA256 -Path $packageProvenancePath
    $checksumArtifacts += @(
        [pscustomobject]@{ Path = $archivePath; SHA256 = $archiveHash },
        [pscustomobject]@{ Path = $packageProvenancePath; SHA256 = $packageProvenanceHash }
    )
    $pendingResults += [pscustomobject]@{
        Architecture = $arch
        StagingPath = $archivePath
        FinalPath = Join-Path $OutputDirectory ([System.IO.Path]::GetFileName($archivePath))
        SHA256 = $archiveHash
        StagingPackageProvenancePath = $packageProvenancePath
        FinalPackageProvenancePath = Join-Path $OutputDirectory ([System.IO.Path]::GetFileName($packageProvenancePath))
        PackageProvenanceSHA256 = $packageProvenanceHash
        SignedExecutableSHA256 = $signedHash
        BuildReceiptSHA256 = $receiptHash
    }
}

$checksumLines = $checksumArtifacts | Sort-Object -Property Path | ForEach-Object {
    "{0}  {1}" -f $_.SHA256, [System.IO.Path]::GetFileName($_.Path)
}
[System.IO.File]::WriteAllText($checksumPath, (($checksumLines -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))
foreach ($artifact in $checksumArtifacts) {
    $expectedLine = "{0}  {1}" -f $artifact.SHA256, [System.IO.Path]::GetFileName($artifact.Path)
    if ([System.IO.File]::ReadAllLines($checksumPath, [System.Text.UTF8Encoding]::new($false)) -cnotcontains $expectedLine) {
        throw "Signed checksum file is missing '$($artifact.Path)'."
    }
    if ((Get-TwinTidyFileSHA256 -Path $artifact.Path) -cne $artifact.SHA256) {
        throw "Signed package artifact changed before checksum publication: $($artifact.Path)"
    }
}

$publishedPaths = @()
try {
    foreach ($pending in $pendingResults) {
        [System.IO.File]::Move($pending.StagingPath, $pending.FinalPath)
        $publishedPaths += $pending.FinalPath
        [System.IO.File]::Move($pending.StagingPackageProvenancePath, $pending.FinalPackageProvenancePath)
        $publishedPaths += $pending.FinalPackageProvenancePath
        if ((Get-TwinTidyFileSHA256 -Path $pending.FinalPath) -cne $pending.SHA256 -or
            (Get-TwinTidyFileSHA256 -Path $pending.FinalPackageProvenancePath) -cne $pending.PackageProvenanceSHA256) {
            throw "Signed package changed while it was being published for $($pending.Architecture)."
        }
        $results += [pscustomobject]@{
            Architecture = $pending.Architecture
            Path = $pending.FinalPath
            SHA256 = $pending.SHA256
            PackageProvenancePath = $pending.FinalPackageProvenancePath
            PackageProvenanceSHA256 = $pending.PackageProvenanceSHA256
            SignedExecutableSHA256 = $pending.SignedExecutableSHA256
            BuildReceiptSHA256 = $pending.BuildReceiptSHA256
        }
    }
    [System.IO.File]::Move($checksumPath, $finalChecksumPath)
    $publishedPaths += $finalChecksumPath
} catch {
    foreach ($publishedPath in $publishedPaths) {
        $fullPublishedPath = [System.IO.Path]::GetFullPath($publishedPath)
        if ([System.IO.Path]::GetDirectoryName($fullPublishedPath) -cne $OutputDirectory) {
            throw "Refusing to remove unexpected partially published file: $fullPublishedPath"
        }
        if ([System.IO.File]::Exists($fullPublishedPath)) { [System.IO.File]::Delete($fullPublishedPath) }
    }
    throw
}

$results
[pscustomobject]@{
    Architecture = "checksums"
    Path = $finalChecksumPath
    SHA256 = Get-TwinTidyFileSHA256 -Path $finalChecksumPath
}
} finally {
    Remove-SignedPackagingStagingDirectory -Path $stagingRoot -ExpectedParent $OutputDirectory
}
