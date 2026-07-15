#requires -Version 5.1

[CmdletBinding()]
param(
    [string]$Version = "dev",

    [ValidateSet("all", "amd64", "arm64")]
    [string[]]$Architecture = @("all"),

    [string]$InputDirectory,

    [string]$OutputDirectory,

    [string]$SourceDate,

    [hashtable]$ExpectedExecutableSHA256,

    [hashtable]$ExpectedBuildReceiptSHA256
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "TwinTidy.Version.ps1")
. (Join-Path $PSScriptRoot "TwinTidy.Release.ps1")

$versionInfo = Resolve-TwinTidyVersion -Version $Version
$repoRoot = Split-Path -Parent $PSScriptRoot
if ($Architecture -contains "all") {
    if ($Architecture.Count -ne 1) {
        throw "Architecture 'all' cannot be combined with another architecture."
    }
    $targets = @("amd64", "arm64")
} else {
    $targets = @($Architecture)
}
if ($null -eq $ExpectedExecutableSHA256) {
    throw "-ExpectedExecutableSHA256 is required; package only a digest captured from the completed build."
}
if ($null -eq $ExpectedBuildReceiptSHA256) {
    throw "-ExpectedBuildReceiptSHA256 is required; package only a receipt captured from the completed build."
}
if ([string]::IsNullOrWhiteSpace($InputDirectory)) {
    $InputDirectory = Join-Path $repoRoot "dist"
} else {
    $InputDirectory = [System.IO.Path]::GetFullPath($InputDirectory)
}
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $InputDirectory "packages"
} else {
    $OutputDirectory = [System.IO.Path]::GetFullPath($OutputDirectory)
}
if ([string]::IsNullOrWhiteSpace($SourceDate)) {
    Push-Location $repoRoot
    try {
        $SourceDate = (& git show -s --format=%cI HEAD).Trim()
        if ($LASTEXITCODE -ne 0) { throw "Unable to resolve the source date." }
    } finally {
        Pop-Location
    }
}

$entryTime = [System.DateTimeOffset]::Parse($SourceDate).ToUniversalTime()
$minimumZipTime = [System.DateTimeOffset]::new(1980, 1, 1, 0, 0, 0, [System.TimeSpan]::Zero)
if ($entryTime -lt $minimumZipTime) {
    throw "ZIP timestamps cannot be earlier than 1980-01-01."
}

function Get-ExpectedSHA256 {
    param(
        [Parameter(Mandatory = $true)][hashtable]$Table,
        [Parameter(Mandatory = $true)][string]$TargetArchitecture,
        [Parameter(Mandatory = $true)][string]$Description
    )

    if (-not $Table.ContainsKey($TargetArchitecture)) {
        throw "$Description is missing an expected SHA-256 for $TargetArchitecture."
    }
    $value = ([string]$Table[$TargetArchitecture]).ToLowerInvariant()
    if ($value -notmatch '^[0-9a-f]{64}$') {
        throw "$Description for $TargetArchitecture is not a SHA-256 digest."
    }
    return $value
}

function Read-LockedUTF8Text {
    param(
        [Parameter(Mandatory = $true)][System.IO.Stream]$Stream,
        [Parameter(Mandatory = $true)][string]$Description
    )

    if (-not $Stream.CanSeek -or $Stream.Length -gt 1MB) {
        throw "$Description is too large or cannot be safely read."
    }
    $Stream.Position = 0
    $bytes = [byte[]]::new([int]$Stream.Length)
    $offset = 0
    while ($offset -lt $bytes.Length) {
        $read = $Stream.Read($bytes, $offset, $bytes.Length - $offset)
        if ($read -eq 0) { throw "Unexpected end of $Description." }
        $offset += $read
    }
    $Stream.Position = 0
    return [System.Text.UTF8Encoding]::new($false, $true).GetString($bytes)
}

function Assert-PortableArchiveBinding {
    param(
        [Parameter(Mandatory = $true)][string]$ArchivePath,
        [Parameter(Mandatory = $true)][string]$ExpectedExecutableHash,
        [Parameter(Mandatory = $true)][string]$ExpectedReceiptHash,
        [Parameter(Mandatory = $true)][string]$ExpectedArchitecture
    )

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [System.IO.Compression.ZipFile]::OpenRead($ArchivePath)
    try {
        $actualEntries = @($archive.Entries | ForEach-Object { $_.FullName } | Sort-Object)
        $expectedEntries = @("LICENSE", "THIRD_PARTY_NOTICES.txt", "TwinTidy.build-receipt.json", "TwinTidy.exe")
        if (($actualEntries -join "|") -cne ($expectedEntries -join "|")) {
            throw "Unexpected portable package entries in $ArchivePath`: $($actualEntries -join ', ')."
        }

        $executableEntry = $archive.GetEntry("TwinTidy.exe")
        $executableStream = $executableEntry.Open()
        try {
            $packagedExecutableHash = Get-TwinTidyStreamSHA256 -Stream $executableStream
        } finally {
            $executableStream.Dispose()
        }
        if ($packagedExecutableHash -cne $ExpectedExecutableHash) {
            throw "Packaged TwinTidy.exe SHA-256 '$packagedExecutableHash' does not match verified '$ExpectedExecutableHash'."
        }

        $receiptEntry = $archive.GetEntry("TwinTidy.build-receipt.json")
        $receiptStream = $receiptEntry.Open()
        try {
            if ($receiptEntry.Length -gt 1MB) {
                throw "Packaged build receipt is too large."
            }
            $receiptBytes = [System.IO.MemoryStream]::new()
            try {
                $receiptStream.CopyTo($receiptBytes)
                $packagedReceiptHash = Get-TwinTidyStreamSHA256 -Stream $receiptBytes
                $receiptText = [System.Text.UTF8Encoding]::new($false, $true).GetString($receiptBytes.ToArray())
            } finally {
                $receiptBytes.Dispose()
            }
        } finally {
            $receiptStream.Dispose()
        }
        if ($packagedReceiptHash -cne $ExpectedReceiptHash) {
            throw "Packaged build-receipt SHA-256 '$packagedReceiptHash' does not match verified '$ExpectedReceiptHash'."
        }
        try {
            $packagedReceipt = ConvertFrom-TwinTidyJson -Json $receiptText
        } catch {
            throw "Packaged build receipt is invalid JSON: $($_.Exception.Message)"
        }
        $null = Assert-TwinTidyBuildReceipt `
            -Receipt $packagedReceipt `
            -ExpectedVersion $versionInfo.Canonical `
            -ExpectedArchitecture $ExpectedArchitecture `
            -ExpectedSourceDate $SourceDate `
            -ExpectedExecutableSHA256 $ExpectedExecutableHash
    } finally {
        $archive.Dispose()
    }
}

Add-Type -AssemblyName System.IO.Compression
[System.IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null
$archives = @()
$licensePath = Join-Path $repoRoot "LICENSE"
if (-not [System.IO.File]::Exists($licensePath)) {
    throw "License file is missing: $licensePath"
}
$noticePath = Join-Path $repoRoot "THIRD_PARTY_NOTICES.txt"
if (-not [System.IO.File]::Exists($noticePath)) {
    throw "Third-party notice file is missing: $noticePath"
}

foreach ($arch in $targets) {
    $expectedExecutableHash = Get-ExpectedSHA256 `
        -Table $ExpectedExecutableSHA256 `
        -TargetArchitecture $arch `
        -Description "Expected executable digest"
    $expectedReceiptHash = Get-ExpectedSHA256 `
        -Table $ExpectedBuildReceiptSHA256 `
        -TargetArchitecture $arch `
        -Description "Expected build-receipt digest"

    $buildDirectory = Join-Path $InputDirectory "TwinTidy-$($versionInfo.Canonical)-windows-$arch"
    $executablePath = Join-Path $buildDirectory "TwinTidy.exe"
    $receiptPath = Join-Path $buildDirectory "TwinTidy.build-receipt.json"
    if (-not [System.IO.File]::Exists($executablePath)) {
        throw "Built executable is missing: $executablePath"
    }
    if (-not [System.IO.File]::Exists($receiptPath)) {
        throw "Build receipt is missing: $receiptPath"
    }

    # FileShare.Read holds both verified inputs immutable through archive creation.
    $receiptStream = [System.IO.File]::Open($receiptPath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
    try {
        $actualReceiptHash = Get-TwinTidyStreamSHA256 -Stream $receiptStream
        if ($actualReceiptHash -cne $expectedReceiptHash) {
            throw "Build-receipt SHA-256 '$actualReceiptHash' does not match expected '$expectedReceiptHash' for $arch."
        }
        $receiptText = Read-LockedUTF8Text -Stream $receiptStream -Description "$arch build receipt"
        try {
            $receipt = ConvertFrom-TwinTidyJson -Json $receiptText
        } catch {
            throw "Build receipt for $arch is invalid JSON: $($_.Exception.Message)"
        }
        $receiptBinding = Assert-TwinTidyBuildReceipt `
            -Receipt $receipt `
            -ExpectedVersion $versionInfo.Canonical `
            -ExpectedArchitecture $arch `
            -ExpectedSourceDate $SourceDate `
            -ExpectedExecutableSHA256 $expectedExecutableHash

        $executableStream = [System.IO.File]::Open($executablePath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
        try {
            $actualExecutableHash = Get-TwinTidyStreamSHA256 -Stream $executableStream
            if ($actualExecutableHash -cne $expectedExecutableHash) {
                throw "Executable SHA-256 '$actualExecutableHash' does not match expected '$expectedExecutableHash' for $arch."
            }
            if ($executableStream.Length -ne $receiptBinding.ExecutableSize) {
                throw "Executable size '$($executableStream.Length)' does not match build receipt '$($receiptBinding.ExecutableSize)' for $arch."
            }

            $licenseStream = [System.IO.File]::Open($licensePath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
            try {
                $noticeStream = [System.IO.File]::Open($noticePath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
                try {
                    $archivePath = Join-Path $OutputDirectory "TwinTidy-$($versionInfo.Canonical)-windows-$arch.zip"
                    if ([System.IO.File]::Exists($archivePath)) {
                        [System.IO.File]::Delete($archivePath)
                    }

                    $archiveCompleted = $false
                    try {
                        $archiveStream = [System.IO.File]::Open($archivePath, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
                        try {
                            $archive = [System.IO.Compression.ZipArchive]::new($archiveStream, [System.IO.Compression.ZipArchiveMode]::Create, $false)
                            try {
                                $packageFiles = @(
                                    [pscustomobject]@{ Stream = $licenseStream; Entry = "LICENSE" }
                                    [pscustomobject]@{ Stream = $noticeStream; Entry = "THIRD_PARTY_NOTICES.txt" }
                                    [pscustomobject]@{ Stream = $receiptStream; Entry = "TwinTidy.build-receipt.json" }
                                    [pscustomobject]@{ Stream = $executableStream; Entry = "TwinTidy.exe" }
                                ) | Sort-Object -Property Entry
                                foreach ($packageFile in $packageFiles) {
                                    $packageFile.Stream.Position = 0
                                    $entry = $archive.CreateEntry($packageFile.Entry, [System.IO.Compression.CompressionLevel]::Optimal)
                                    $entry.LastWriteTime = $entryTime
                                    $entryStream = $entry.Open()
                                    try {
                                        $packageFile.Stream.CopyTo($entryStream)
                                    } finally {
                                        $entryStream.Dispose()
                                    }
                                }
                            } finally {
                                $archive.Dispose()
                            }
                        } finally {
                            $archiveStream.Dispose()
                        }
                        $archiveCompleted = $true
                    } finally {
                        if (-not $archiveCompleted -and [System.IO.File]::Exists($archivePath)) {
                            [System.IO.File]::Delete($archivePath)
                        }
                    }
                } finally {
                    $noticeStream.Dispose()
                }
            } finally {
                $licenseStream.Dispose()
            }
        } finally {
            $executableStream.Dispose()
        }
    } finally {
        $receiptStream.Dispose()
    }

    Assert-PortableArchiveBinding `
        -ArchivePath $archivePath `
        -ExpectedExecutableHash $expectedExecutableHash `
        -ExpectedReceiptHash $expectedReceiptHash `
        -ExpectedArchitecture $arch

    $archiveHash = Get-TwinTidyFileSHA256 -Path $archivePath
    $archives += [pscustomobject]@{
        Architecture = $arch
        Path = $archivePath
        SHA256 = $archiveHash
        ExecutableSHA256 = $expectedExecutableHash
        BuildReceiptSHA256 = $expectedReceiptHash
        SourceTreeSHA256 = $receiptBinding.SourceTreeSHA256
        Commit = $receiptBinding.Commit
    }
}

$checksumPath = Join-Path $OutputDirectory "SHA256SUMS.txt"
$checksumLines = $archives |
    Sort-Object -Property Path |
    ForEach-Object { "{0}  {1}" -f $_.SHA256, [System.IO.Path]::GetFileName($_.Path) }
$checksumContent = ($checksumLines -join "`n") + "`n"
[System.IO.File]::WriteAllText($checksumPath, $checksumContent, [System.Text.UTF8Encoding]::new($false))

$writtenChecksumLines = [System.IO.File]::ReadAllLines($checksumPath, [System.Text.UTF8Encoding]::new($false))
if ($writtenChecksumLines.Count -ne $archives.Count) {
    throw "SHA256SUMS.txt does not contain exactly one entry per package."
}
foreach ($archiveResult in $archives) {
    $expectedLine = "{0}  {1}" -f $archiveResult.SHA256, [System.IO.Path]::GetFileName($archiveResult.Path)
    if ($writtenChecksumLines -cnotcontains $expectedLine) {
        throw "SHA256SUMS.txt is missing the verified digest binding for '$($archiveResult.Path)'."
    }
    $currentArchiveHash = Get-TwinTidyFileSHA256 -Path $archiveResult.Path
    if ($currentArchiveHash -cne $archiveResult.SHA256) {
        throw "Archive '$($archiveResult.Path)' changed before checksum verification."
    }
}

$archives
[pscustomobject]@{
    Architecture = "checksums"
    Path = $checksumPath
    SHA256 = Get-TwinTidyFileSHA256 -Path $checksumPath
}
