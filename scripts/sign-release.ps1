#requires -Version 5.1

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,

    [ValidateSet("all", "amd64", "arm64")]
    [string[]]$Architecture = @("all"),

    [string]$InputDirectory,

    [string]$OutputDirectory,

    [string]$SourceDate,

    [Parameter(Mandatory = $true)][string]$ExpectedCommit,

    [Parameter(Mandatory = $true)][string]$ExpectedGitTree,

    [Parameter(Mandatory = $true)][string]$ExpectedSourceTreeSHA256,

    [Parameter(Mandatory = $true)][string]$SignerAdapter,

    [Parameter(Mandatory = $true)][string]$ExpectedSignerSubject,

    [string]$ExpectedSignerThumbprint,

    [ValidateSet("fixed-certificate", "artifact-signing")]
    [string]$SignerVerificationMode = "fixed-certificate",

    [string]$ExpectedSignerIdentityEku,

    [Parameter(Mandatory = $true)][hashtable]$ExpectedUnsignedExecutableSHA256,

    [Parameter(Mandatory = $true)][hashtable]$ExpectedBuildReceiptSHA256
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "TwinTidy.Version.ps1")
. (Join-Path $PSScriptRoot "TwinTidy.Release.ps1")

$versionInfo = Resolve-TwinTidyVersion -Version $Version
if ($versionInfo.Canonical -ceq "dev") {
    throw "Development builds are not public signing inputs; provide a SemVer release version."
}

$repoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($InputDirectory)) {
    $InputDirectory = Join-Path $repoRoot "dist"
}
$InputDirectory = [System.IO.Path]::GetFullPath($InputDirectory)
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $InputDirectory "signed"
}
$OutputDirectory = [System.IO.Path]::GetFullPath($OutputDirectory)
$adapterPath = [System.IO.Path]::GetFullPath($SignerAdapter)
if (-not [System.IO.File]::Exists($adapterPath)) {
    throw "Signer adapter is missing: $adapterPath"
}
if ([string]::IsNullOrWhiteSpace($ExpectedSignerSubject)) {
    throw "-ExpectedSignerSubject must be the exact certificate subject."
}
if ($SignerVerificationMode -ceq "fixed-certificate") {
    $ExpectedSignerThumbprint = Normalize-TwinTidyCertificateThumbprint -Thumbprint $ExpectedSignerThumbprint
} elseif ([string]::IsNullOrWhiteSpace($ExpectedSignerIdentityEku)) {
    throw "Artifact Signing requires -ExpectedSignerIdentityEku."
}
if ($ExpectedCommit -notmatch '^[0-9a-fA-F]{40,64}$' -or
    $ExpectedGitTree -notmatch '^[0-9a-fA-F]{40,64}$' -or
    $ExpectedSourceTreeSHA256 -notmatch '^[0-9a-fA-F]{64}$') {
    throw "Expected commit, Git tree, and source-tree SHA-256 are invalid."
}
$ExpectedCommit = $ExpectedCommit.ToLowerInvariant()
$ExpectedGitTree = $ExpectedGitTree.ToLowerInvariant()
$ExpectedSourceTreeSHA256 = $ExpectedSourceTreeSHA256.ToLowerInvariant()

if ($Architecture -contains "all") {
    if ($Architecture.Count -ne 1) {
        throw "Architecture 'all' cannot be combined with another architecture."
    }
    $targets = @("amd64", "arm64")
} else {
    $targets = @($Architecture)
}

if ([string]::IsNullOrWhiteSpace($SourceDate)) {
    Push-Location $repoRoot
    try {
        $SourceDate = (& git show -s --format=%cI HEAD 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($SourceDate)) {
            throw "Unable to resolve the source date."
        }
    } finally {
        Pop-Location
    }
}
[void][System.DateTimeOffset]::Parse($SourceDate)

function Get-ExpectedDigest {
    param(
        [Parameter(Mandatory = $true)][hashtable]$Table,
        [Parameter(Mandatory = $true)][string]$TargetArchitecture,
        [Parameter(Mandatory = $true)][string]$Description
    )

    if (-not $Table.ContainsKey($TargetArchitecture)) {
        throw "$Description is missing an expected SHA-256 for $TargetArchitecture."
    }
    $digest = ([string]$Table[$TargetArchitecture]).ToLowerInvariant()
    if ($digest -notmatch '^[0-9a-f]{64}$') {
        throw "$Description for $TargetArchitecture is not a SHA-256 digest."
    }
    return $digest
}

function Remove-SigningStagingDirectory {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$ExpectedParent
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $fullParent = [System.IO.Path]::GetFullPath($ExpectedParent)
    if ([System.IO.Path]::GetDirectoryName($fullPath) -cne $fullParent -or
        -not [System.IO.Path]::GetFileName($fullPath).StartsWith(".signing-", [System.StringComparison]::Ordinal)) {
        throw "Refusing to remove unexpected signing staging directory: $fullPath"
    }
    if ([System.IO.Directory]::Exists($fullPath)) {
        [System.IO.Directory]::Delete($fullPath, $true)
    }
}

function Remove-PublishedSignedDirectory {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$ExpectedParent,
        [Parameter(Mandatory = $true)][string]$ExpectedLeaf
    )

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    if ([System.IO.Path]::GetDirectoryName($fullPath) -cne [System.IO.Path]::GetFullPath($ExpectedParent) -or
        [System.IO.Path]::GetFileName($fullPath) -cne $ExpectedLeaf) {
        throw "Refusing to remove unexpected signed output directory: $fullPath"
    }
    if ([System.IO.Directory]::Exists($fullPath)) {
        [System.IO.Directory]::Delete($fullPath, $true)
    }
}

[System.IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null
$stagingRoot = Join-Path $OutputDirectory (".signing-" + [System.Guid]::NewGuid().ToString("N"))
[System.IO.Directory]::CreateDirectory($stagingRoot) | Out-Null
$results = @()
$pendingPublications = @()

try {
    foreach ($arch in $targets) {
        $unsignedHash = Get-ExpectedDigest -Table $ExpectedUnsignedExecutableSHA256 -TargetArchitecture $arch -Description "Expected unsigned executable digest"
        $receiptHash = Get-ExpectedDigest -Table $ExpectedBuildReceiptSHA256 -TargetArchitecture $arch -Description "Expected build-receipt digest"
        $artifactName = "TwinTidy-$($versionInfo.Canonical)-windows-$arch"
        $inputArtifact = Join-Path $InputDirectory $artifactName
        $unsignedPath = Join-Path $inputArtifact "TwinTidy.exe"
        $receiptPath = Join-Path $inputArtifact "TwinTidy.build-receipt.json"
        $finalArtifact = Join-Path $OutputDirectory $artifactName
        $stagingArtifact = Join-Path $stagingRoot $artifactName
        $signedPath = Join-Path $stagingArtifact "TwinTidy.exe"
        $stagedReceiptPath = Join-Path $stagingArtifact "TwinTidy.build-receipt.json"
        $provenancePath = Join-Path $stagingArtifact "TwinTidy.signed-provenance.json"

        if ([System.IO.Directory]::Exists($finalArtifact) -or [System.IO.File]::Exists($finalArtifact)) {
            throw "Refusing to replace existing signed output: $finalArtifact"
        }
        if (-not [System.IO.File]::Exists($unsignedPath)) { throw "Unsigned executable is missing: $unsignedPath" }
        if (-not [System.IO.File]::Exists($receiptPath)) { throw "Build receipt is missing: $receiptPath" }
        foreach ($inputPath in @($unsignedPath, $receiptPath)) {
            if (([System.IO.FileInfo]::new($inputPath).Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Release input must not be a reparse point: $inputPath"
            }
        }

        [System.IO.Directory]::CreateDirectory($stagingArtifact) | Out-Null
        $unsignedStream = [System.IO.File]::Open($unsignedPath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
        try {
            $actualUnsignedHash = Get-TwinTidyStreamSHA256 -Stream $unsignedStream
            if ($actualUnsignedHash -cne $unsignedHash) {
                throw "Unsigned executable SHA-256 '$actualUnsignedHash' does not match expected '$unsignedHash' for $arch."
            }

            $receiptStream = [System.IO.File]::Open($receiptPath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
            try {
                $actualReceiptHash = Get-TwinTidyStreamSHA256 -Stream $receiptStream
                if ($actualReceiptHash -cne $receiptHash) {
                    throw "Build-receipt SHA-256 '$actualReceiptHash' does not match expected '$receiptHash' for $arch."
                }
                if ($receiptStream.Length -gt 1MB) { throw "Build receipt for $arch is too large." }
                $receiptBytes = [byte[]]::new([int]$receiptStream.Length)
                $receiptStream.Position = 0
                $offset = 0
                while ($offset -lt $receiptBytes.Length) {
                    $read = $receiptStream.Read($receiptBytes, $offset, $receiptBytes.Length - $offset)
                    if ($read -eq 0) { throw "Unexpected end of build receipt for $arch." }
                    $offset += $read
                }
                try {
                    $receipt = ConvertFrom-TwinTidyJson -Json ([System.Text.UTF8Encoding]::new($false, $true).GetString($receiptBytes))
                } catch {
                    throw "Build receipt for $arch is invalid JSON: $($_.Exception.Message)"
                }
                $receiptBinding = Assert-TwinTidyBuildReceipt `
                    -Receipt $receipt `
                    -ExpectedVersion $versionInfo.Canonical `
                    -ExpectedArchitecture $arch `
                    -ExpectedSourceDate $SourceDate `
                    -ExpectedExecutableSHA256 $unsignedHash
                if ($receiptBinding.SourceKind -cne "git-commit" -or
                    -not $receiptBinding.SourceClean -or
                    $receiptBinding.Commit -cne $ExpectedCommit -or
                    $receiptBinding.GitTree -cne $ExpectedGitTree -or
                    $receiptBinding.SourceTreeSHA256 -cne $ExpectedSourceTreeSHA256) {
                    throw "Build receipt for $arch is not bound to the validated clean release commit and Git tree."
                }
                if ($unsignedStream.Length -ne $receiptBinding.ExecutableSize) {
                    throw "Unsigned executable size does not match the immutable build receipt for $arch."
                }

                $adapterSucceeded = $false
                $LASTEXITCODE = 0
                & $adapterPath -InputPath $unsignedPath -OutputPath $signedPath
                $adapterSucceeded = $?
                if (-not $adapterSucceeded -or $LASTEXITCODE -ne 0) {
                    throw "Signer adapter failed for $arch with exit code $LASTEXITCODE."
                }
                if (-not [System.IO.File]::Exists($signedPath)) {
                    throw "Signer adapter did not create its declared output for ${arch}: $signedPath"
                }
                if (([System.IO.FileInfo]::new($signedPath).Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                    throw "Signer adapter output must not be a reparse point: $signedPath"
                }

                $signedHash = Get-TwinTidyFileSHA256 -Path $signedPath
                if ($signedHash -ceq $unsignedHash) {
                    throw "Signer adapter output for $arch is byte-identical to the unsigned executable."
                }
                $signature = Assert-TwinTidyAuthenticodeSignature `
                    -Path $signedPath `
                    -ExpectedSignerSubject $ExpectedSignerSubject `
                    -ExpectedSignerThumbprint $ExpectedSignerThumbprint `
                    -SignerVerificationMode $SignerVerificationMode `
                    -ExpectedSignerIdentityEku $ExpectedSignerIdentityEku `
                    -RequireTimestamp $true

                [System.IO.File]::WriteAllBytes($stagedReceiptPath, $receiptBytes)
                if ((Get-TwinTidyFileSHA256 -Path $stagedReceiptPath) -cne $receiptHash) {
                    throw "Copied build receipt changed while staging signed output for $arch."
                }

                $provenance = [ordered]@{
                    schema = "twintidy.signed-provenance/v1"
                    product = "TwinTidy"
                    version = $versionInfo.Canonical
                    architecture = $arch
                    sourceDate = $SourceDate
                    generatedAt = [System.DateTimeOffset]::UtcNow.ToString("o")
                    source = [ordered]@{
                        commit = $receiptBinding.Commit
                        gitTree = $receiptBinding.GitTree
                        treeSHA256 = $receiptBinding.SourceTreeSHA256
                    }
                    unsigned = [ordered]@{
                        path = "TwinTidy.exe"
                        sha256 = $unsignedHash
                        size = [int64]$receiptBinding.ExecutableSize
                        buildReceiptSHA256 = $receiptHash
                    }
                    signed = [ordered]@{
                        path = "TwinTidy.exe"
                        sha256 = $signedHash
                        size = [int64]([System.IO.FileInfo]::new($signedPath).Length)
                    }
                    authenticode = [ordered]@{
                        status = $signature.Status
                        signerSubject = $signature.SignerSubject
                        signerThumbprint = $signature.SignerThumbprint
                        verificationMode = $signature.VerificationMode
                        signerIdentityEku = $signature.SignerIdentityEku
                        signerNotBefore = $signature.SignerNotBefore
                        signerNotAfter = $signature.SignerNotAfter
                        timestamped = $signature.Timestamped
                        timestampSubject = $signature.TimestampSubject
                        timestampThumbprint = $signature.TimestampThumbprint
                    }
                }
                $provenanceJson = ($provenance | ConvertTo-Json -Depth 8) + "`n"
                [System.IO.File]::WriteAllText($provenancePath, $provenanceJson, [System.Text.UTF8Encoding]::new($false))

                $parsedProvenance = ConvertFrom-TwinTidyJson -Json ([System.IO.File]::ReadAllText($provenancePath, [System.Text.UTF8Encoding]::new($false, $true)))
                $null = Assert-TwinTidySignedProvenance `
                    -Provenance $parsedProvenance `
                    -ExpectedVersion $versionInfo.Canonical `
                    -ExpectedArchitecture $arch `
                    -ExpectedSourceDate $SourceDate `
                    -ExpectedCommit $ExpectedCommit `
                    -ExpectedGitTree $ExpectedGitTree `
                    -ExpectedSourceTreeSHA256 $ExpectedSourceTreeSHA256 `
                    -ExpectedUnsignedExecutableSHA256 $unsignedHash `
                    -ExpectedBuildReceiptSHA256 $receiptHash `
                    -ExpectedSignedExecutableSHA256 $signedHash `
                    -ExpectedSignerSubject $ExpectedSignerSubject `
                    -ExpectedSignerThumbprint $ExpectedSignerThumbprint `
                    -SignerVerificationMode $SignerVerificationMode `
                    -ExpectedSignerIdentityEku $ExpectedSignerIdentityEku
            } finally {
                $receiptStream.Dispose()
            }
        } finally {
            $unsignedStream.Dispose()
        }

        $pendingPublications += [pscustomobject]@{
            Architecture = $arch
            ArtifactName = $artifactName
            StagingArtifact = $stagingArtifact
            FinalArtifact = $finalArtifact
            UnsignedPath = $unsignedPath
            UnsignedExecutableSHA256 = $unsignedHash
            ReceiptPath = $receiptPath
            SignedExecutableSHA256 = $signedHash
            BuildReceiptSHA256 = $receiptHash
            SignedProvenanceSHA256 = Get-TwinTidyFileSHA256 -Path $provenancePath
            SignerCertificateSHA256 = $signature.SignerThumbprint
            SignerIdentityEku = $signature.SignerIdentityEku
        }
    }

    $publishedArtifacts = @()
    try {
        foreach ($publication in $pendingPublications) {
            [System.IO.Directory]::Move($publication.StagingArtifact, $publication.FinalArtifact)
            $publishedArtifacts += $publication
            $finalSignedPath = Join-Path $publication.FinalArtifact "TwinTidy.exe"
            $finalReceiptPath = Join-Path $publication.FinalArtifact "TwinTidy.build-receipt.json"
            $finalProvenancePath = Join-Path $publication.FinalArtifact "TwinTidy.signed-provenance.json"
            if ((Get-TwinTidyFileSHA256 -Path $publication.UnsignedPath) -cne $publication.UnsignedExecutableSHA256 -or
                (Get-TwinTidyFileSHA256 -Path $publication.ReceiptPath) -cne $publication.BuildReceiptSHA256) {
                throw "Unsigned signing input changed during signing for $($publication.Architecture)."
            }
            if ((Get-TwinTidyFileSHA256 -Path $finalSignedPath) -cne $publication.SignedExecutableSHA256 -or
                (Get-TwinTidyFileSHA256 -Path $finalReceiptPath) -cne $publication.BuildReceiptSHA256 -or
                (Get-TwinTidyFileSHA256 -Path $finalProvenancePath) -cne $publication.SignedProvenanceSHA256) {
                throw "Signed output changed while it was being published for $($publication.Architecture)."
            }
            $null = Assert-TwinTidyAuthenticodeSignature `
                -Path $finalSignedPath `
                -ExpectedSignerSubject $ExpectedSignerSubject `
                -ExpectedSignerThumbprint $ExpectedSignerThumbprint `
                -SignerVerificationMode $SignerVerificationMode `
                -ExpectedSignerIdentityEku $ExpectedSignerIdentityEku `
                -RequireTimestamp $true

            $results += [pscustomobject]@{
                Architecture = $publication.Architecture
                Path = $publication.FinalArtifact
                SignedExecutableSHA256 = $publication.SignedExecutableSHA256
                BuildReceiptSHA256 = $publication.BuildReceiptSHA256
                SignedProvenanceSHA256 = $publication.SignedProvenanceSHA256
                SignerSubject = $ExpectedSignerSubject
                SignerThumbprint = $publication.SignerCertificateSHA256
                SignerIdentityEku = $publication.SignerIdentityEku
                SignerVerificationMode = $SignerVerificationMode
            }
        }
    } catch {
        foreach ($publication in $publishedArtifacts) {
            Remove-PublishedSignedDirectory `
                -Path $publication.FinalArtifact `
                -ExpectedParent $OutputDirectory `
                -ExpectedLeaf $publication.ArtifactName
        }
        throw
    }
} finally {
    Remove-SigningStagingDirectory -Path $stagingRoot -ExpectedParent $OutputDirectory
}

$results
