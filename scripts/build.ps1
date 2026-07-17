#requires -Version 5.1

[CmdletBinding()]
param(
    [string]$Version = "dev",

    [string]$Commit,

    [string]$SourceDate,

    [ValidateSet("all", "amd64", "arm64")]
    [string[]]$Architecture = @("all"),

    [string]$OutputDirectory,

    [switch]$SkipResourceCheck,

    [ValidateSet("WorkingTree", "GitCommit")]
    [string]$SourceMode = "WorkingTree"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "TwinTidy.Version.ps1")
. (Join-Path $PSScriptRoot "TwinTidy.Release.ps1")

$versionInfo = Resolve-TwinTidyVersion -Version $Version
$repoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $repoRoot "dist"
} else {
    $OutputDirectory = [System.IO.Path]::GetFullPath($OutputDirectory)
}

if ($Architecture -contains "all") {
    if ($Architecture.Count -ne 1) {
        throw "Architecture 'all' cannot be combined with another architecture."
    }
    $targets = @("amd64", "arm64")
} else {
    $targets = @($Architecture)
}

function Remove-VerifiedBuildStage {
    param([Parameter(Mandatory = $true)][string]$Path)

    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $tempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    if (-not $tempRoot.EndsWith([System.IO.Path]::DirectorySeparatorChar.ToString())) {
        $tempRoot += [System.IO.Path]::DirectorySeparatorChar
    }
    if (-not $fullPath.StartsWith($tempRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to remove a build stage outside the system temp root: $fullPath"
    }
    if (-not ([System.IO.Path]::GetFileName($fullPath)).StartsWith("TwinTidyBuildStage-", [System.StringComparison]::Ordinal)) {
        throw "Refusing to remove an unexpected build stage: $fullPath"
    }
    if ([System.IO.Directory]::Exists($fullPath)) {
        [System.IO.Directory]::Delete($fullPath, $true)
    }
}

function Copy-CurrentSourceTree {
    param([Parameter(Mandatory = $true)][string]$Destination)

    [System.IO.Directory]::CreateDirectory($Destination) | Out-Null
    $rootPrefix = [System.IO.Path]::GetFullPath($repoRoot)
    if (-not $rootPrefix.EndsWith([System.IO.Path]::DirectorySeparatorChar.ToString())) {
        $rootPrefix += [System.IO.Path]::DirectorySeparatorChar
    }
    $destinationPrefix = [System.IO.Path]::GetFullPath($Destination)
    if (-not $destinationPrefix.EndsWith([System.IO.Path]::DirectorySeparatorChar.ToString())) {
        $destinationPrefix += [System.IO.Path]::DirectorySeparatorChar
    }

    $sourceFiles = @(& git ls-files --cached --others --exclude-standard) | Sort-Object -Unique
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to enumerate the current source tree."
    }
    foreach ($relativePath in $sourceFiles) {
        $sourcePath = [System.IO.Path]::GetFullPath((Join-Path $repoRoot $relativePath))
        if (-not $sourcePath.StartsWith($rootPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "Source path escapes the repository: $relativePath"
        }
        if (-not [System.IO.File]::Exists($sourcePath)) {
            continue
        }

        $destinationPath = [System.IO.Path]::GetFullPath((Join-Path $Destination $relativePath))
        if (-not $destinationPath.StartsWith($destinationPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
            throw "Destination path escapes the build stage: $relativePath"
        }
        [System.IO.Directory]::CreateDirectory((Split-Path -Parent $destinationPath)) | Out-Null
        [System.IO.File]::Copy($sourcePath, $destinationPath, $true)
    }
}

function Copy-GitCommitSourceTree {
    param(
        [Parameter(Mandatory = $true)][string]$Destination,
        [Parameter(Mandatory = $true)][string]$ResolvedCommit,
        [Parameter(Mandatory = $true)][string]$StageRoot
    )

    $submodules = @(& git ls-tree -r $ResolvedCommit | Where-Object { $_ -match '^160000 ' })
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to inspect commit '$ResolvedCommit' for submodules."
    }
    if ($submodules.Count -gt 0) {
        throw "Release builds do not support Git submodules; source identity would be incomplete."
    }

    [System.IO.Directory]::CreateDirectory($Destination) | Out-Null
    $archivePath = Join-Path $StageRoot "source.zip"
    & git archive --format=zip --output=$archivePath $ResolvedCommit
    if ($LASTEXITCODE -ne 0 -or -not [System.IO.File]::Exists($archivePath)) {
        throw "Unable to archive source commit '$ResolvedCommit'."
    }
    try {
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        [System.IO.Compression.ZipFile]::ExtractToDirectory($archivePath, $Destination)
    } finally {
        if ([System.IO.File]::Exists($archivePath)) {
            [System.IO.File]::Delete($archivePath)
        }
    }
}

Push-Location $repoRoot
try {
    $requestedCommit = if ([string]::IsNullOrWhiteSpace($Commit)) { "HEAD" } else { $Commit }
    $sourceIdentity = Resolve-TwinTidyGitSourceIdentity `
        -RepositoryRoot $repoRoot `
        -Commit $requestedCommit `
        -SourceDate $SourceDate
    if ($SourceMode -eq "WorkingTree" -and $sourceIdentity.Commit -cne $sourceIdentity.HeadCommit) {
        throw "A working-tree build must be based on HEAD; use -SourceMode GitCommit to build an earlier commit."
    }
    $Commit = $sourceIdentity.Commit
    $SourceDate = $sourceIdentity.SourceDate

    $workingTreeStatus = @(& git status --porcelain=v1 --untracked-files=all --)
    if ($LASTEXITCODE -ne 0) { throw "Unable to inspect the working-tree state." }
    $workingTreeClean = $workingTreeStatus.Count -eq 0

    if (-not $SkipResourceCheck) {
        $null = & (Join-Path $PSScriptRoot "generate-resources.ps1") -Architecture all -Check
    }

    $stageRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("TwinTidyBuildStage-" + [System.Guid]::NewGuid().ToString("N"))
    $stageSource = Join-Path $stageRoot "source"
    try {
        if ($SourceMode -eq "GitCommit") {
            Copy-GitCommitSourceTree -Destination $stageSource -ResolvedCommit $Commit -StageRoot $stageRoot
            $sourceKind = "git-commit"
            $sourceClean = $true
        } else {
            Copy-CurrentSourceTree -Destination $stageSource
            $sourceKind = "working-tree"
            $sourceClean = $workingTreeClean
        }
        $sourceTreeDigest = Get-TwinTidySourceTreeDigest -SourceRoot $stageSource
        $runtimeCommit = $Commit
        if ($SourceMode -eq "WorkingTree" -and -not $sourceClean) {
            $runtimeCommit = "$Commit+working-tree.$($sourceTreeDigest.SHA256.Substring(0, 12))"
        }

        $stageResourcePrefix = Join-Path $stageSource "cmd\twintidy\rsrc"
        $null = & (Join-Path $PSScriptRoot "generate-resources.ps1") `
            -Architecture $targets `
            -SourceRoot $stageSource `
            -OutputPrefix $stageResourcePrefix `
            -Version $versionInfo.Canonical

        $goVersion = (& go version 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($goVersion)) {
            throw "Unable to identify the Go toolchain."
        }

        $previousCGO = $env:CGO_ENABLED
        $previousGOOS = $env:GOOS
        $previousGOARCH = $env:GOARCH
        try {
            Push-Location $stageSource
            try {
                foreach ($arch in $targets) {
                    $env:CGO_ENABLED = "0"
                    $env:GOOS = "windows"
                    $env:GOARCH = $arch

                    $targetDirectory = Join-Path $OutputDirectory "TwinTidy-$($versionInfo.Canonical)-windows-$arch"
                    [System.IO.Directory]::CreateDirectory($targetDirectory) | Out-Null
                    $executablePath = Join-Path $targetDirectory "TwinTidy.exe"
                    $receiptPath = Join-Path $targetDirectory "TwinTidy.build-receipt.json"
                    foreach ($stalePath in @($executablePath, $receiptPath)) {
                        if ([System.IO.File]::Exists($stalePath)) {
                            [System.IO.File]::Delete($stalePath)
                        }
                    }

                    $ldflags = @(
                        "-buildid="
                        "-H=windowsgui"
                        "-s"
                        "-w"
                        "-X github.com/Soyuz-Tec/twintidy/internal/buildinfo.Version=$($versionInfo.Canonical)"
                        "-X github.com/Soyuz-Tec/twintidy/internal/buildinfo.Commit=$runtimeCommit"
                        "-X github.com/Soyuz-Tec/twintidy/internal/buildinfo.SourceDate=$SourceDate"
                    ) -join " "

                    & go build -mod=readonly -trimpath -buildvcs=false -ldflags $ldflags -o $executablePath ./cmd/twintidy
                    if ($LASTEXITCODE -ne 0) {
                        throw "TwinTidy $arch build failed with exit code $LASTEXITCODE."
                    }

                    $executableHash = Get-TwinTidyFileSHA256 -Path $executablePath
                    $executableSize = ([System.IO.FileInfo]::new($executablePath)).Length
                    $resourcePath = Join-Path $stageSource "cmd\twintidy\rsrc_windows_$arch.syso"
                    $resourceHash = Get-TwinTidyFileSHA256 -Path $resourcePath
                    $receipt = [ordered]@{
                        schema = "twintidy.build-receipt/v1"
                        product = "TwinTidy"
                        version = $versionInfo.Canonical
                        architecture = $arch
                        sourceDate = $SourceDate
                        source = [ordered]@{
                            kind = $sourceKind
                            commit = $Commit
                            gitTree = $sourceIdentity.GitTree
                            clean = $sourceClean
                            treeDigestAlgorithm = $sourceTreeDigest.Algorithm
                            treeSHA256 = $sourceTreeDigest.SHA256
                            fileCount = $sourceTreeDigest.FileCount
                        }
                        build = [ordered]@{
                            goVersion = $goVersion
                            goos = "windows"
                            goarch = $arch
                            cgoEnabled = $false
                            trimpath = $true
                            buildVCS = $false
                            resourceSHA256 = $resourceHash
                        }
                        executable = [ordered]@{
                            path = "TwinTidy.exe"
                            size = $executableSize
                            sha256 = $executableHash
                        }
                    }
                    $receiptJson = $receipt | ConvertTo-Json -Depth 10
                    [System.IO.File]::WriteAllText($receiptPath, $receiptJson + "`n", [System.Text.UTF8Encoding]::new($false))
                    $receiptHash = Get-TwinTidyFileSHA256 -Path $receiptPath

                    [pscustomobject]@{
                        Architecture = $arch
                        Path = $executablePath
                        SHA256 = $executableHash
                        ReceiptPath = $receiptPath
                        ReceiptSHA256 = $receiptHash
                        SourceKind = $sourceKind
                        SourceTreeSHA256 = $sourceTreeDigest.SHA256
                        GitTree = $sourceIdentity.GitTree
                        CGOEnabled = $env:CGO_ENABLED
                        Version = $versionInfo.Canonical
                        PEVersion = $versionInfo.PEVersion
                        Commit = $Commit
                        RuntimeCommit = $runtimeCommit
                        SourceDate = $SourceDate
                    }
                }
            } finally {
                Pop-Location
            }
        } finally {
            $env:CGO_ENABLED = $previousCGO
            $env:GOOS = $previousGOOS
            $env:GOARCH = $previousGOARCH
        }
    } finally {
        Remove-VerifiedBuildStage -Path $stageRoot
    }
} finally {
    Pop-Location
}
