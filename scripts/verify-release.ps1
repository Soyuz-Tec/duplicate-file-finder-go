#requires -Version 5.1

[CmdletBinding()]
param(
    [string]$Version = "dev",

    [switch]$RunNativeSmoke,

    [string]$VerifiedOutputDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "TwinTidy.Version.ps1")
. (Join-Path $PSScriptRoot "TwinTidy.Release.ps1")

$versionInfo = Resolve-TwinTidyVersion -Version $Version
$tool = "github.com/tc-hib/go-winres@v0.3.3"
$repoRoot = Split-Path -Parent $PSScriptRoot
$commit = ""
$sourceDate = ""

function Remove-VerifiedTempDirectory {
    param([Parameter(Mandatory = $true)][string]$Path)

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $tempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    if (-not $tempRoot.EndsWith([System.IO.Path]::DirectorySeparatorChar.ToString())) {
        $tempRoot += [System.IO.Path]::DirectorySeparatorChar
    }
    if (-not $fullPath.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to remove a directory outside the system temp root: $fullPath"
    }
    if (-not ([System.IO.Path]::GetFileName($fullPath)).StartsWith("TwinTidyVerify-", [System.StringComparison]::Ordinal)) {
        throw "Refusing to remove an unexpected temp directory: $fullPath"
    }
    if ([System.IO.Directory]::Exists($fullPath)) {
        [System.IO.Directory]::Delete($fullPath, $true)
    }
}

function Copy-VerifiedReleaseFile {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination,
        [Parameter(Mandatory = $true)][string]$ExpectedSHA256
    )

    $input = [System.IO.File]::Open($Source, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
    try {
        $actualSourceHash = Get-TwinTidyStreamSHA256 -Stream $input
        if ($actualSourceHash -cne $ExpectedSHA256) {
            throw "Verified release source '$Source' changed before export."
        }
        $output = [System.IO.File]::Open($Destination, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
        try {
            $input.Position = 0
            $input.CopyTo($output)
            $output.Flush($true)
        } finally {
            $output.Dispose()
        }
    } finally {
        $input.Dispose()
    }
    $actualDestinationHash = Get-TwinTidyFileSHA256 -Path $Destination
    if ($actualDestinationHash -cne $ExpectedSHA256) {
        throw "Verified release export '$Destination' does not match '$ExpectedSHA256'."
    }
}

function Get-PeMachine {
    param([Parameter(Mandatory = $true)][string]$Path)

    $bytes = [System.IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 64 -or $bytes[0] -ne 0x4d -or $bytes[1] -ne 0x5a) {
        throw "Not a valid PE file: $Path"
    }
    $peOffset = [System.BitConverter]::ToInt32($bytes, 0x3c)
    if ($peOffset -lt 0 -or ($peOffset + 6) -gt $bytes.Length) {
        throw "Invalid PE header offset in $Path"
    }
    if ($bytes[$peOffset] -ne 0x50 -or $bytes[$peOffset + 1] -ne 0x45) {
        throw "Missing PE signature in $Path"
    }
    return [System.BitConverter]::ToUInt16($bytes, $peOffset + 4)
}

function Assert-ExtractedResources {
    param(
        [Parameter(Mandatory = $true)][string]$ExecutablePath,
        [Parameter(Mandatory = $true)][string]$Architecture,
        [Parameter(Mandatory = $true)][string]$ExtractionDirectory,
        [Parameter(Mandatory = $true)]$ExpectedVersion
    )

    [System.IO.Directory]::CreateDirectory($ExtractionDirectory) | Out-Null
    & go run $tool extract --dir $ExtractionDirectory --xml-manifest $ExecutablePath
    if ($LASTEXITCODE -ne 0) {
        throw "Resource extraction failed for $Architecture."
    }

    $configPath = Get-ChildItem -LiteralPath $ExtractionDirectory -Recurse -File -Filter "winres.json" | Select-Object -First 1 -ExpandProperty FullName
    if ([string]::IsNullOrWhiteSpace($configPath)) {
        throw "Extracted resource configuration is missing for $Architecture."
    }
    $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    foreach ($property in @("RT_GROUP_ICON", "RT_MANIFEST", "RT_VERSION")) {
        if ($config.PSObject.Properties.Name -notcontains $property) {
            throw "Extracted resources for $Architecture are missing $property."
        }
    }
    $iconFiles = @(Get-ChildItem -LiteralPath $ExtractionDirectory -Recurse -File | Where-Object { $_.Extension -in @(".ico", ".png") })
    if ($iconFiles.Count -eq 0) {
        throw "Extracted icon resource is missing for $Architecture."
    }

    $manifestPath = $null
    $manifestDocument = $null
    $manifestFiles = Get-ChildItem -LiteralPath $ExtractionDirectory -Recurse -File |
        Where-Object { $_.Extension -in @(".xml", ".manifest") }
    foreach ($manifestFile in $manifestFiles) {
        try {
            $candidateDocument = Read-TwinTidyXmlDocument -Path $manifestFile.FullName
        } catch {
            continue
        }
        $commonControls = @($candidateDocument.SelectNodes("//*[local-name()='assemblyIdentity']") | Where-Object {
            $_ -is [System.Xml.XmlElement] -and $_.GetAttribute("name") -ceq "Microsoft.Windows.Common-Controls"
        })
        if ($commonControls.Count -gt 0) {
            if ($null -ne $manifestPath) {
                throw "Extracted resources for $Architecture contain more than one Common Controls manifest."
            }
            $manifestPath = $manifestFile.FullName
            $manifestDocument = $candidateDocument
        }
    }
    if ([string]::IsNullOrWhiteSpace($manifestPath)) {
        throw "Extracted Common Controls manifest is missing for $Architecture."
    }
    $null = Assert-TwinTidyManifestPolicy -Path $manifestPath
    $dpiAwareness = @($manifestDocument.SelectNodes("//*[local-name()='dpiAwareness']") | Where-Object {
        $_.InnerText.Trim() -ceq "PerMonitorV2, PerMonitor"
    })
    if ($dpiAwareness.Count -ne 1) {
        throw "Extracted manifest for $Architecture must contain one PerMonitorV2, PerMonitor dpiAwareness setting."
    }
    $longPathAware = @($manifestDocument.SelectNodes("//*[local-name()='longPathAware']") | Where-Object {
        $_.InnerText.Trim() -ceq "true"
    })
    if ($longPathAware.Count -ne 1) {
        throw "Extracted manifest for $Architecture must contain one enabled longPathAware setting."
    }

    $version = [System.Diagnostics.FileVersionInfo]::GetVersionInfo($ExecutablePath)
    if ($version.ProductName -ne "TwinTidy") {
        throw "ProductName for $Architecture is '$($version.ProductName)', expected TwinTidy."
    }
    if ($version.OriginalFilename -ne "TwinTidy.exe") {
        throw "OriginalFilename for $Architecture is '$($version.OriginalFilename)', expected TwinTidy.exe."
    }
    if ($version.FileDescription -ne "TwinTidy - Safe Duplicate File Review") {
        throw "Unexpected FileDescription for ${Architecture}: '$($version.FileDescription)'."
    }
    if ($version.CompanyName -ne "Kayilan Inc") {
        throw "CompanyName for $Architecture is '$($version.CompanyName)', expected Kayilan Inc."
    }
    if ($version.LegalCopyright -ne "Copyright (c) 2026 Kayilan Inc") {
        throw "Unexpected LegalCopyright for ${Architecture}: '$($version.LegalCopyright)'."
    }
    if ($version.FileVersion.Trim() -ne $ExpectedVersion.Canonical) {
        throw "FileVersion string for $Architecture is '$($version.FileVersion)', expected '$($ExpectedVersion.Canonical)'."
    }
    if ($version.ProductVersion.Trim() -ne $ExpectedVersion.Canonical) {
        throw "ProductVersion string for $Architecture is '$($version.ProductVersion)', expected '$($ExpectedVersion.Canonical)'."
    }

    $fileParts = @($version.FileMajorPart, $version.FileMinorPart, $version.FileBuildPart, $version.FilePrivatePart)
    $productParts = @($version.ProductMajorPart, $version.ProductMinorPart, $version.ProductBuildPart, $version.ProductPrivatePart)
    for ($index = 0; $index -lt 4; $index++) {
        if ($fileParts[$index] -ne $ExpectedVersion.NumericParts[$index]) {
            throw "Numeric FileVersion for $Architecture does not equal $($ExpectedVersion.PEVersion)."
        }
        if ($productParts[$index] -ne $ExpectedVersion.NumericParts[$index]) {
            throw "Numeric ProductVersion for $Architecture does not equal $($ExpectedVersion.PEVersion)."
        }
    }
}

function Test-RuntimeVersion {
    param(
        [Parameter(Mandatory = $true)][string]$ExecutablePath,
        [Parameter(Mandatory = $true)][string]$ExpectedVersion,
        [Parameter(Mandatory = $true)][string]$ExpectedCommit,
        [Parameter(Mandatory = $true)][string]$ExpectedSourceDate
    )

    $output = (& $ExecutablePath --version 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "Runtime version check failed with exit code $LASTEXITCODE for $ExecutablePath."
    }
    $expected = "TwinTidy $ExpectedVersion (commit $ExpectedCommit, source date $ExpectedSourceDate)"
    if ($output -ne $expected) {
        throw "Runtime version output '$output' does not equal '$expected'."
    }
}

function Assert-PackageContents {
    param(
        [Parameter(Mandatory = $true)][string]$ArchivePath,
        [Parameter(Mandatory = $true)][string]$ExpectedExecutableSHA256,
        [Parameter(Mandatory = $true)][string]$ExpectedBuildReceiptSHA256,
        [Parameter(Mandatory = $true)][string]$ExpectedArchitecture
    )

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [System.IO.Compression.ZipFile]::OpenRead($ArchivePath)
    try {
        $actual = @($archive.Entries | ForEach-Object { $_.FullName } | Sort-Object)
        $expected = @("LICENSE", "THIRD_PARTY_NOTICES.txt", "TwinTidy.build-receipt.json", "TwinTidy.exe")
        if (($actual -join "|") -ne ($expected -join "|")) {
            throw "Unexpected portable package entries in $ArchivePath`: $($actual -join ', ')."
        }

        $executableStream = $archive.GetEntry("TwinTidy.exe").Open()
        try {
            $actualExecutableHash = Get-TwinTidyStreamSHA256 -Stream $executableStream
        } finally {
            $executableStream.Dispose()
        }
        if ($actualExecutableHash -cne $ExpectedExecutableSHA256) {
            throw "Packaged executable does not match its independently verified digest."
        }

        $receiptEntry = $archive.GetEntry("TwinTidy.build-receipt.json")
        if ($receiptEntry.Length -gt 1MB) {
            throw "Packaged build receipt is too large."
        }
        $receiptStream = $receiptEntry.Open()
        try {
            $receiptMemory = [System.IO.MemoryStream]::new()
            try {
                $receiptStream.CopyTo($receiptMemory)
                $actualReceiptHash = Get-TwinTidyStreamSHA256 -Stream $receiptMemory
                $receiptText = [System.Text.UTF8Encoding]::new($false, $true).GetString($receiptMemory.ToArray())
            } finally {
                $receiptMemory.Dispose()
            }
        } finally {
            $receiptStream.Dispose()
        }
        if ($actualReceiptHash -cne $ExpectedBuildReceiptSHA256) {
            throw "Packaged build receipt does not match its independently verified digest."
        }
        try {
            $receipt = ConvertFrom-TwinTidyJson -Json $receiptText
        } catch {
            throw "Packaged build receipt is invalid JSON: $($_.Exception.Message)"
        }
        $null = Assert-TwinTidyBuildReceipt `
            -Receipt $receipt `
            -ExpectedVersion $versionInfo.Canonical `
            -ExpectedArchitecture $ExpectedArchitecture `
            -ExpectedSourceDate $sourceDate `
            -ExpectedExecutableSHA256 $ExpectedExecutableSHA256
    } finally {
        $archive.Dispose()
    }
}

Push-Location $repoRoot
try {
    $null = Assert-TwinTidyReleaseSourceClean -RepositoryRoot $repoRoot
    $sourceIdentity = Resolve-TwinTidyGitSourceIdentity -RepositoryRoot $repoRoot -Commit HEAD
    $commit = $sourceIdentity.Commit
    $sourceDate = $sourceIdentity.SourceDate

    $null = Assert-TwinTidyManifestPolicy -Path (Join-Path $repoRoot "cmd\twintidy\twintidy.manifest")
    $null = & (Join-Path $PSScriptRoot "generate-resources.ps1") -Architecture all -Check
    $null = & (Join-Path $PSScriptRoot "generate-notices.ps1") -Check

    $trackedResourceHashes = @{}
    foreach ($arch in @("amd64", "arm64")) {
        $trackedPath = Join-Path $repoRoot "cmd\twintidy\rsrc_windows_$arch.syso"
        $trackedResourceHashes[$arch] = (Get-FileHash -LiteralPath $trackedPath -Algorithm SHA256).Hash
    }

    $tempDirectory = Join-Path ([System.IO.Path]::GetTempPath()) ("TwinTidyVerify-" + [System.Guid]::NewGuid().ToString("N"))
    $firstBuild = Join-Path $tempDirectory "first-build"
    $secondBuild = Join-Path $tempDirectory "second-build"
    $firstPackages = Join-Path $tempDirectory "first-packages"
    $secondPackages = Join-Path $tempDirectory "second-packages"

    try {
        $firstBuildResults = @(& (Join-Path $PSScriptRoot "build.ps1") `
            -Version $versionInfo.Canonical `
            -Commit $commit `
            -SourceDate $sourceDate `
            -Architecture all `
            -OutputDirectory $firstBuild `
            -SkipResourceCheck `
            -SourceMode GitCommit)
        $secondBuildResults = @(& (Join-Path $PSScriptRoot "build.ps1") `
            -Version $versionInfo.Canonical `
            -Commit $commit `
            -SourceDate $sourceDate `
            -Architecture all `
            -OutputDirectory $secondBuild `
            -SkipResourceCheck `
            -SourceMode GitCommit)

        $firstExecutableHashes = @{}
        $firstReceiptHashes = @{}
        $secondExecutableHashes = @{}
        $secondReceiptHashes = @{}
        $firstReceiptDocuments = @{}
        foreach ($arch in @("amd64", "arm64")) {
            $firstResult = @($firstBuildResults | Where-Object { $_.Architecture -ceq $arch })
            $secondResult = @($secondBuildResults | Where-Object { $_.Architecture -ceq $arch })
            if ($firstResult.Count -ne 1 -or $secondResult.Count -ne 1) {
                throw "Build result set does not contain exactly one receipt for $arch."
            }
            $firstExecutableHashes[$arch] = ([string]$firstResult[0].SHA256).ToLowerInvariant()
            $firstReceiptHashes[$arch] = ([string]$firstResult[0].ReceiptSHA256).ToLowerInvariant()
            $secondExecutableHashes[$arch] = ([string]$secondResult[0].SHA256).ToLowerInvariant()
            $secondReceiptHashes[$arch] = ([string]$secondResult[0].ReceiptSHA256).ToLowerInvariant()
        }

        $expectedMachines = @{ amd64 = [uint16]0x8664; arm64 = [uint16]0xaa64 }
        foreach ($arch in @("amd64", "arm64")) {
            $firstExe = Join-Path (Join-Path $firstBuild "TwinTidy-$($versionInfo.Canonical)-windows-$arch") "TwinTidy.exe"
            $secondExe = Join-Path (Join-Path $secondBuild "TwinTidy-$($versionInfo.Canonical)-windows-$arch") "TwinTidy.exe"
            $firstHash = (Get-FileHash -LiteralPath $firstExe -Algorithm SHA256).Hash
            $secondHash = (Get-FileHash -LiteralPath $secondExe -Algorithm SHA256).Hash
            if ($firstHash.ToLowerInvariant() -cne $firstExecutableHashes[$arch] -or $secondHash.ToLowerInvariant() -cne $secondExecutableHashes[$arch]) {
                throw "Build result digest does not match the completed executable for $arch."
            }
            if ($firstExecutableHashes[$arch] -cne $secondExecutableHashes[$arch]) {
                throw "Unsigned $arch executable is not reproducible ($firstHash != $secondHash)."
            }

            $firstReceiptPath = Join-Path (Split-Path -Parent $firstExe) "TwinTidy.build-receipt.json"
            $secondReceiptPath = Join-Path (Split-Path -Parent $secondExe) "TwinTidy.build-receipt.json"
            $firstReceiptHash = Get-TwinTidyFileSHA256 -Path $firstReceiptPath
            $secondReceiptHash = Get-TwinTidyFileSHA256 -Path $secondReceiptPath
            if ($firstReceiptHash -cne $firstReceiptHashes[$arch] -or $secondReceiptHash -cne $secondReceiptHashes[$arch]) {
                throw "Build result digest does not match the completed build receipt for $arch."
            }
            if ($firstReceiptHashes[$arch] -cne $secondReceiptHashes[$arch]) {
                throw "Build receipt for $arch is not reproducible ($firstReceiptHash != $secondReceiptHash)."
            }
            $firstReceipt = ConvertFrom-TwinTidyJson -Json (Get-Content -LiteralPath $firstReceiptPath -Raw)
            $firstReceiptDocuments[$arch] = $firstReceipt
            $firstBinding = Assert-TwinTidyBuildReceipt `
                -Receipt $firstReceipt `
                -ExpectedVersion $versionInfo.Canonical `
                -ExpectedArchitecture $arch `
                -ExpectedSourceDate $sourceDate `
                -ExpectedExecutableSHA256 $firstExecutableHashes[$arch]
            if ($firstBinding.SourceKind -cne "git-commit" -or
                -not $firstBinding.SourceClean -or
                $firstBinding.Commit -cne $commit -or
                $firstBinding.GitTree -cne $sourceIdentity.GitTree) {
                throw "Build receipt for $arch is not bound to the verified Git source identity."
            }

            $machine = Get-PeMachine -Path $firstExe
            if ($machine -ne $expectedMachines[$arch]) {
                throw ("PE machine for {0} is 0x{1:x4}, expected 0x{2:x4}." -f $arch, $machine, $expectedMachines[$arch])
            }

            Assert-ExtractedResources -ExecutablePath $firstExe -Architecture $arch -ExtractionDirectory (Join-Path $tempDirectory "extract-$arch") -ExpectedVersion $versionInfo

            $hostArchitecture = $env:PROCESSOR_ARCHITECTURE.ToUpperInvariant()
            $canRun = ($arch -eq "amd64" -and $hostArchitecture -in @("AMD64", "ARM64")) -or ($arch -eq "arm64" -and $hostArchitecture -eq "ARM64")
            if ($canRun) {
                Test-RuntimeVersion -ExecutablePath $firstExe -ExpectedVersion $versionInfo.Canonical -ExpectedCommit $commit -ExpectedSourceDate $sourceDate
            } else {
                Write-Host "Skipping $arch runtime version execution on $hostArchitecture host; native CI must verify it."
            }

            if ($RunNativeSmoke) {
                if ($canRun) {
                    $process = Start-Process -FilePath $firstExe -ArgumentList @("--ui-smoke-test") -Wait -PassThru -WindowStyle Hidden
                    if ($process.ExitCode -ne 0) {
                        throw "Native $arch UI smoke test failed with exit code $($process.ExitCode)."
                    }
                } else {
                    Write-Host "Skipping $arch UI smoke on $hostArchitecture host."
                }
            }

            [pscustomobject]@{
                Architecture = $arch
                ReproducibleExecutable = $true
                SHA256 = $firstHash
                PEMachine = ("0x{0:x4}" -f $machine)
                ResourcesVerified = $true
                Version = $versionInfo.Canonical
                PEVersion = $versionInfo.PEVersion
                RuntimeVersionVerified = $canRun
            }
        }

        $packageHashes = @{}
        foreach ($arch in @("amd64", "arm64")) {
            $trackedPath = Join-Path $repoRoot "cmd\twintidy\rsrc_windows_$arch.syso"
            $afterHash = (Get-FileHash -LiteralPath $trackedPath -Algorithm SHA256).Hash
            if ($afterHash -ne $trackedResourceHashes[$arch]) {
                throw "Version-specific build modified the tracked $arch development resource."
            }
        }

        $null = & (Join-Path $PSScriptRoot "package.ps1") `
            -Version $versionInfo.Canonical `
            -Architecture all `
            -InputDirectory $firstBuild `
            -OutputDirectory $firstPackages `
            -SourceDate $sourceDate `
            -ExpectedExecutableSHA256 $firstExecutableHashes `
            -ExpectedBuildReceiptSHA256 $firstReceiptHashes
        $null = & (Join-Path $PSScriptRoot "package.ps1") `
            -Version $versionInfo.Canonical `
            -Architecture all `
            -InputDirectory $secondBuild `
            -OutputDirectory $secondPackages `
            -SourceDate $sourceDate `
            -ExpectedExecutableSHA256 $secondExecutableHashes `
            -ExpectedBuildReceiptSHA256 $secondReceiptHashes

        foreach ($arch in @("amd64", "arm64")) {
            $firstArchive = Join-Path $firstPackages "TwinTidy-$($versionInfo.Canonical)-windows-$arch.zip"
            $secondArchive = Join-Path $secondPackages "TwinTidy-$($versionInfo.Canonical)-windows-$arch.zip"
            $firstHash = (Get-FileHash -LiteralPath $firstArchive -Algorithm SHA256).Hash
            $secondHash = (Get-FileHash -LiteralPath $secondArchive -Algorithm SHA256).Hash
            if ($firstHash -ne $secondHash) {
                throw "Portable $arch package is not reproducible ($firstHash != $secondHash)."
            }
            $packageHashes[$arch] = $firstHash.ToLowerInvariant()
            Assert-PackageContents `
                -ArchivePath $firstArchive `
                -ExpectedExecutableSHA256 $firstExecutableHashes[$arch] `
                -ExpectedBuildReceiptSHA256 $firstReceiptHashes[$arch] `
                -ExpectedArchitecture $arch
            [pscustomobject]@{
                Architecture = $arch
                ReproduciblePackage = $true
                SHA256 = $firstHash
            }
        }
        $firstChecksums = Join-Path $firstPackages "SHA256SUMS.txt"
        $secondChecksums = Join-Path $secondPackages "SHA256SUMS.txt"
        $firstChecksumHash = (Get-FileHash -LiteralPath $firstChecksums -Algorithm SHA256).Hash
        $secondChecksumHash = (Get-FileHash -LiteralPath $secondChecksums -Algorithm SHA256).Hash
        if ($firstChecksumHash -ne $secondChecksumHash) {
            throw "SHA256SUMS.txt is not reproducible ($firstChecksumHash != $secondChecksumHash)."
        }
        [pscustomobject]@{
            Architecture = "checksums"
            ReproduciblePackage = $true
            SHA256 = $firstChecksumHash
        }

        if (-not [string]::IsNullOrWhiteSpace($VerifiedOutputDirectory)) {
            $verifiedOutput = [System.IO.Path]::GetFullPath($VerifiedOutputDirectory)
            if ([System.IO.Directory]::Exists($verifiedOutput)) {
                if (@([System.IO.Directory]::EnumerateFileSystemEntries($verifiedOutput)).Count -ne 0) {
                    throw "Verified output directory must be empty: $verifiedOutput"
                }
            } else {
                [System.IO.Directory]::CreateDirectory($verifiedOutput) | Out-Null
            }

            $architectureReceipts = @()
            foreach ($arch in @("amd64", "arm64")) {
                $sourceDirectory = Join-Path $firstBuild "TwinTidy-$($versionInfo.Canonical)-windows-$arch"
                $outputDirectory = Join-Path $verifiedOutput "TwinTidy-$($versionInfo.Canonical)-windows-$arch"
                [System.IO.Directory]::CreateDirectory($outputDirectory) | Out-Null
                Copy-VerifiedReleaseFile `
                    -Source (Join-Path $sourceDirectory "TwinTidy.exe") `
                    -Destination (Join-Path $outputDirectory "TwinTidy.exe") `
                    -ExpectedSHA256 $firstExecutableHashes[$arch]
                Copy-VerifiedReleaseFile `
                    -Source (Join-Path $sourceDirectory "TwinTidy.build-receipt.json") `
                    -Destination (Join-Path $outputDirectory "TwinTidy.build-receipt.json") `
                    -ExpectedSHA256 $firstReceiptHashes[$arch]

                $architectureReceipts += [ordered]@{
                    architecture = $arch
                    unsignedExecutable = [ordered]@{
                        path = "TwinTidy-$($versionInfo.Canonical)-windows-$arch/TwinTidy.exe"
                        size = ([System.IO.FileInfo]::new((Join-Path $outputDirectory "TwinTidy.exe"))).Length
                        sha256 = $firstExecutableHashes[$arch]
                    }
                    buildReceipt = [ordered]@{
                        path = "TwinTidy-$($versionInfo.Canonical)-windows-$arch/TwinTidy.build-receipt.json"
                        sha256 = $firstReceiptHashes[$arch]
                    }
                    replicas = @(
                        [ordered]@{
                            ordinal = 1
                            executableSHA256 = $firstExecutableHashes[$arch]
                            buildReceiptSHA256 = $firstReceiptHashes[$arch]
                            packageSHA256 = $packageHashes[$arch]
                        },
                        [ordered]@{
                            ordinal = 2
                            executableSHA256 = $secondExecutableHashes[$arch]
                            buildReceiptSHA256 = $secondReceiptHashes[$arch]
                            packageSHA256 = $packageHashes[$arch]
                        }
                    )
                    reproducible = $true
                }
            }

            $sourceReceipt = $firstReceiptDocuments["amd64"].source
            $reproducibilityReceipt = [ordered]@{
                schema = "twintidy.unsigned-reproducibility/v1"
                product = "TwinTidy"
                version = $versionInfo.Canonical
                sourceDate = $sourceDate
                source = [ordered]@{
                    commit = $commit
                    gitTree = $sourceIdentity.GitTree
                    treeDigestAlgorithm = [string]$sourceReceipt.treeDigestAlgorithm
                    treeSHA256 = [string]$sourceReceipt.treeSHA256
                    fileCount = [int]$sourceReceipt.fileCount
                }
                architectures = $architectureReceipts
            }
            $reproducibilityPath = Join-Path $verifiedOutput "TwinTidy.unsigned-reproducibility.json"
            [System.IO.File]::WriteAllText(
                $reproducibilityPath,
                (($reproducibilityReceipt | ConvertTo-Json -Depth 12) + "`n"),
                [System.Text.UTF8Encoding]::new($false)
            )
            [pscustomobject]@{
                Architecture = "all"
                VerifiedOutput = $verifiedOutput
                ReproducibilityReceiptPath = $reproducibilityPath
                ReproducibilityReceiptSHA256 = Get-TwinTidyFileSHA256 -Path $reproducibilityPath
            }
        }
    } finally {
        Remove-VerifiedTempDirectory -Path $tempDirectory
    }
} finally {
    Pop-Location
}
