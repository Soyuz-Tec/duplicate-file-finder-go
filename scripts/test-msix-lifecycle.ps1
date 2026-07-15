#requires -Version 5.1

[CmdletBinding()]
param(
    [ValidateSet("amd64", "arm64")]
    [string]$Architecture = $(if ($env:PROCESSOR_ARCHITECTURE -ceq "ARM64") { "arm64" } else { "amd64" }),

    [int]$SmokeIterations = 3
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot "TwinTidy.Release.ps1")

$repoRoot = Split-Path -Parent $PSScriptRoot
$packageName = "KayilanInc.TwinTidy.CITest"
$versions = @("0.1.0-beta.1", "0.1.0-beta.2")
$sourceIdentity = Resolve-TwinTidyGitSourceIdentity -RepositoryRoot $repoRoot -Commit HEAD
$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("TwinTidyMSIXLifecycle-" + [System.Guid]::NewGuid().ToString("N"))
$certificate = $null
$trustedCertificate = $null

function Resolve-TwinTidyWindowsSDKTool {
    param([Parameter(Mandatory = $true)][string]$Name)

    $kitsRoot = Join-Path ${env:ProgramFiles(x86)} "Windows Kits\10\bin"
    $candidates = @(Get-ChildItem -LiteralPath $kitsRoot -Filter $Name -File -Recurse -ErrorAction SilentlyContinue |
        Where-Object { $_.Directory.Name -ceq "x64" } |
        Sort-Object -Property FullName -Descending)
    if ($candidates.Count -eq 0) {
        throw "$Name was not found in the installed Windows SDK."
    }
    return $candidates[0].FullName
}

function Remove-TwinTidyTestPackage {
    $packages = @(Get-AppxPackage -Name $packageName -ErrorAction SilentlyContinue)
    foreach ($package in $packages) {
        Remove-AppxPackage -Package $package.PackageFullName -ErrorAction Stop
    }
}

function Assert-TwinTidyInstalledVersion {
    param(
        [Parameter(Mandatory = $true)][string]$Version,
        [Parameter(Mandatory = $true)][string]$Commit,
        [Parameter(Mandatory = $true)][string]$SourceDate,
        [Parameter(Mandatory = $true)][int]$SmokeCount
    )

    $packages = @(Get-AppxPackage -Name $packageName)
    if ($packages.Count -ne 1) {
        throw "Expected one installed TwinTidy MSIX package; found $($packages.Count)."
    }
    $executable = Join-Path $packages[0].InstallLocation "TwinTidy.exe"
    $actualVersion = (& $executable --version 2>&1 | Out-String).Trim()
    $expectedVersion = "TwinTidy $Version (commit $Commit, source date $SourceDate)"
    if ($actualVersion -cne $expectedVersion) {
        throw "Installed runtime version '$actualVersion' does not equal '$expectedVersion'."
    }
    for ($attempt = 1; $attempt -le $SmokeCount; $attempt++) {
        $process = Start-Process -FilePath $executable -ArgumentList @("--ui-smoke-test") -Wait -PassThru -WindowStyle Hidden
        if ($process.ExitCode -ne 0) {
            throw "Installed MSIX UI smoke attempt $attempt failed with exit code $($process.ExitCode)."
        }
    }
}

[System.IO.Directory]::CreateDirectory($tempRoot) | Out-Null
try {
    $signTool = Resolve-TwinTidyWindowsSDKTool -Name "signtool.exe"
    $makeAppx = Resolve-TwinTidyWindowsSDKTool -Name "makeappx.exe"
    $subject = "CN=Kayilan Inc TwinTidy CI Test"
    $certificate = New-SelfSignedCertificate `
        -Type Custom `
        -Subject $subject `
        -FriendlyName "TwinTidy disposable MSIX lifecycle test" `
        -CertStoreLocation "Cert:\CurrentUser\My" `
        -KeyAlgorithm RSA `
        -KeyLength 2048 `
        -HashAlgorithm SHA256 `
        -KeyUsage DigitalSignature `
        -TextExtension @("2.5.29.37={text}1.3.6.1.5.5.7.3.3", "2.5.29.19={text}") `
        -NotAfter (Get-Date).AddDays(2)
    $certificatePath = Join-Path $tempRoot "TwinTidy-MSIX-Test.cer"
    Export-Certificate -Cert $certificate -FilePath $certificatePath -Type CERT | Out-Null
    $trustedCertificate = Import-Certificate -FilePath $certificatePath -CertStoreLocation "Cert:\LocalMachine\TrustedPeople"
    $certificateSHA256 = Get-TwinTidyCertificateSHA256 -Certificate $certificate

    $signedInputs = Join-Path $tempRoot "signed-inputs"
    $msixOutput = Join-Path $tempRoot "msix"
    $signedPackages = @{}

    foreach ($version in $versions) {
        $buildRoot = Join-Path $tempRoot "build-$version"
        $buildResults = @(& (Join-Path $PSScriptRoot "build.ps1") `
            -Version $version `
            -Commit $sourceIdentity.Commit `
            -SourceDate $sourceIdentity.SourceDate `
            -Architecture $Architecture `
            -OutputDirectory $buildRoot `
            -SkipResourceCheck `
            -SourceMode GitCommit)
        $build = @($buildResults | Where-Object { $_.Architecture -ceq $Architecture })
        if ($build.Count -ne 1) {
            throw "Lifecycle build did not produce exactly one $Architecture result for $version."
        }

        $sourceDirectory = Join-Path $buildRoot "TwinTidy-$version-windows-$Architecture"
        $inputDirectory = Join-Path $signedInputs "TwinTidy-$version-windows-$Architecture"
        [System.IO.Directory]::CreateDirectory($inputDirectory) | Out-Null
        $unsignedExecutable = Join-Path $sourceDirectory "TwinTidy.exe"
        $signedExecutable = Join-Path $inputDirectory "TwinTidy.exe"
        [System.IO.File]::Copy($unsignedExecutable, $signedExecutable, $false)
        [System.IO.File]::Copy((Join-Path $sourceDirectory "TwinTidy.build-receipt.json"), (Join-Path $inputDirectory "TwinTidy.build-receipt.json"), $false)

        & $signTool sign /fd SHA256 /sha1 $certificate.Thumbprint /s My $signedExecutable
        if ($LASTEXITCODE -ne 0) {
            throw "SignTool failed to sign the $Architecture lifecycle executable for $version."
        }
        $signature = Get-AuthenticodeSignature -LiteralPath $signedExecutable
        if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid) {
            throw "Lifecycle executable signature is not valid for $version`: $($signature.StatusMessage)"
        }

        $provenance = [ordered]@{
            schema = "twintidy.signed-provenance/v1"
            product = "TwinTidy"
            version = $version
            architecture = $Architecture
            source = [ordered]@{
                commit = $sourceIdentity.Commit
                gitTree = $sourceIdentity.GitTree
                sourceDate = $sourceIdentity.SourceDate
            }
            unsigned = [ordered]@{
                path = "TwinTidy.exe"
                sha256 = ([string]$build[0].SHA256).ToLowerInvariant()
                size = ([System.IO.FileInfo]::new($unsignedExecutable)).Length
                buildReceiptSHA256 = ([string]$build[0].ReceiptSHA256).ToLowerInvariant()
            }
            signed = [ordered]@{
                path = "TwinTidy.exe"
                sha256 = Get-TwinTidyFileSHA256 -Path $signedExecutable
                size = ([System.IO.FileInfo]::new($signedExecutable)).Length
            }
            authenticode = [ordered]@{
                status = "Valid"
                signerSubject = $certificate.Subject
                signerThumbprint = $certificateSHA256
                verificationMode = "fixed-certificate"
                signerIdentityEku = $null
                timestamped = $false
                timestampSubject = $null
                timestampThumbprint = $null
            }
        }
        [System.IO.File]::WriteAllText(
            (Join-Path $inputDirectory "TwinTidy.signed-provenance.json"),
            (($provenance | ConvertTo-Json -Depth 10) + "`n"),
            [System.Text.UTF8Encoding]::new($false)
        )

        $packageResults = @(& (Join-Path $PSScriptRoot "package-msix.ps1") `
            -Version $version `
            -Architecture $Architecture `
            -InputDirectory $signedInputs `
            -OutputDirectory $msixOutput `
            -Publisher $certificate.Subject `
            -ExpectedSignerThumbprint $certificateSHA256 `
            -PackageName $packageName `
            -AllowTestSignatureWithoutTimestamp `
            -MakeAppxPath $makeAppx)
        $package = @($packageResults | Where-Object { $_.Architecture -ceq $Architecture })
        if ($package.Count -ne 1) {
            throw "Lifecycle packaging did not produce exactly one $Architecture MSIX for $version."
        }
        $signedPackagePath = ([System.IO.Path]::GetFullPath($package[0].Path)) -replace '\.unsigned\.msix$', '.msix'
        [System.IO.File]::Copy($package[0].Path, $signedPackagePath, $false)
        & $signTool sign /fd SHA256 /sha1 $certificate.Thumbprint /s My $signedPackagePath
        if ($LASTEXITCODE -ne 0) {
            throw "SignTool failed to sign the $Architecture lifecycle MSIX for $version."
        }
        $packageSignature = Get-AuthenticodeSignature -LiteralPath $signedPackagePath
        if ($packageSignature.Status -ne [System.Management.Automation.SignatureStatus]::Valid) {
            throw "Lifecycle MSIX signature is not valid for $version`: $($packageSignature.StatusMessage)"
        }
        $signedPackages[$version] = $signedPackagePath
    }

    Remove-TwinTidyTestPackage
    Add-AppxPackage -Path $signedPackages[$versions[0]] -ForceApplicationShutdown
    Assert-TwinTidyInstalledVersion -Version $versions[0] -Commit $sourceIdentity.Commit -SourceDate $sourceIdentity.SourceDate -SmokeCount $SmokeIterations

    Add-AppxPackage -Path $signedPackages[$versions[1]] -ForceApplicationShutdown
    Assert-TwinTidyInstalledVersion -Version $versions[1] -Commit $sourceIdentity.Commit -SourceDate $sourceIdentity.SourceDate -SmokeCount $SmokeIterations

    $downgradeRejected = $false
    try {
        Add-AppxPackage -Path $signedPackages[$versions[0]] -ForceApplicationShutdown -ErrorAction Stop
    } catch {
        $downgradeRejected = $true
    }
    if (-not $downgradeRejected) {
        throw "Windows accepted a lower-version TwinTidy MSIX over the installed newer package."
    }

    Remove-TwinTidyTestPackage
    if (@(Get-AppxPackage -Name $packageName -ErrorAction SilentlyContinue).Count -ne 0) {
        throw "TwinTidy MSIX remained installed after uninstall."
    }

    Add-AppxPackage -Path $signedPackages[$versions[0]] -ForceApplicationShutdown
    Assert-TwinTidyInstalledVersion -Version $versions[0] -Commit $sourceIdentity.Commit -SourceDate $sourceIdentity.SourceDate -SmokeCount 1
    Remove-TwinTidyTestPackage

    Add-AppxPackage -Path $signedPackages[$versions[1]] -ForceApplicationShutdown
    Assert-TwinTidyInstalledVersion -Version $versions[1] -Commit $sourceIdentity.Commit -SourceDate $sourceIdentity.SourceDate -SmokeCount 1
    Remove-TwinTidyTestPackage

    [pscustomobject]@{
        Architecture = $Architecture
        FreshInstall = $true
        Upgrade = $true
        DowngradeRejected = $true
        RollbackAfterUninstall = $true
        RuntimeVersion = $true
        UISmoke = $true
        Uninstall = $true
        Reinstall = $true
        Publisher = $certificate.Subject
    }
} finally {
    Remove-TwinTidyTestPackage
    if ($null -ne $trustedCertificate) {
        Remove-Item -LiteralPath ("Cert:\LocalMachine\TrustedPeople\" + $trustedCertificate.Thumbprint) -Force -ErrorAction SilentlyContinue
    }
    if ($null -ne $certificate) {
        Remove-Item -LiteralPath ("Cert:\CurrentUser\My\" + $certificate.Thumbprint) -Force -ErrorAction SilentlyContinue
    }

    $fullTemp = [System.IO.Path]::GetFullPath($tempRoot)
    $systemTemp = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    if (-not $systemTemp.EndsWith([System.IO.Path]::DirectorySeparatorChar.ToString())) {
        $systemTemp += [System.IO.Path]::DirectorySeparatorChar
    }
    if (-not $fullTemp.StartsWith($systemTemp, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not [System.IO.Path]::GetFileName($fullTemp).StartsWith("TwinTidyMSIXLifecycle-", [System.StringComparison]::Ordinal)) {
        throw "Refusing to remove unexpected MSIX lifecycle directory: $fullTemp"
    }
    if ([System.IO.Directory]::Exists($fullTemp)) {
        [System.IO.Directory]::Delete($fullTemp, $true)
    }
}
