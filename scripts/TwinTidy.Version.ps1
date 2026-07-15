#requires -Version 5.1

Set-StrictMode -Version Latest

function Resolve-TwinTidyVersion {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Version)

    if ($Version -eq "dev") {
        return [pscustomobject]@{
            Canonical = "dev"
            PEVersion = "0.0.0.0"
            MSIXVersion = "1.0.0.0"
            NumericParts = [uint16[]]@(0, 0, 0, 0)
            IsPrerelease = $true
            SpecialBuild = "Development"
        }
    }

    $match = [System.Text.RegularExpressions.Regex]::Match(
        $Version,
        "^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$",
        [System.Text.RegularExpressions.RegexOptions]::CultureInvariant
    )
    if (-not $match.Success) {
        throw "Version '$Version' must be 'dev' or SemVer without build metadata, for example 1.2.3 or 1.2.3-beta.1."
    }

    $numeric = @()
    foreach ($groupIndex in 1..3) {
        $value = [uint64]::Parse($match.Groups[$groupIndex].Value, [System.Globalization.CultureInfo]::InvariantCulture)
        if ($value -gt [uint16]::MaxValue) {
            throw "Version '$Version' contains a numeric component larger than 65535."
        }
        $numeric += [uint16]$value
    }

    $prerelease = $match.Groups[4].Value
    $fourthPart = [uint16]0
    $specialBuild = ""
    if (-not [string]::IsNullOrWhiteSpace($prerelease)) {
        $identifiers = $prerelease.Split(".")
        foreach ($identifier in $identifiers) {
            if ($identifier -match "^[0-9]+$" -and $identifier.Length -gt 1 -and $identifier.StartsWith("0", [System.StringComparison]::Ordinal)) {
                throw "Version '$Version' has a numeric prerelease identifier with a leading zero."
            }
        }

        $sequence = $identifiers[$identifiers.Length - 1]
        if ($sequence -notmatch "^[0-9]+$") {
            throw "Prerelease version '$Version' must end in a numeric sequence, for example beta.1, so PE mapping is unambiguous."
        }
        $sequenceValue = [uint64]::Parse($sequence, [System.Globalization.CultureInfo]::InvariantCulture)
        if ($sequenceValue -eq 0 -or $sequenceValue -gt [uint16]::MaxValue) {
            throw "Prerelease sequence in '$Version' must be between 1 and 65535."
        }
        $fourthPart = [uint16]$sequenceValue
        $specialBuild = $prerelease
    }

    $canonical = "{0}.{1}.{2}" -f $numeric[0], $numeric[1], $numeric[2]
    if (-not [string]::IsNullOrWhiteSpace($prerelease)) {
        $canonical += "-$prerelease"
    }

    $msixVersion = $null
    if ($numeric[0] -lt [uint16]::MaxValue) {
        $msixVersion = ("{0}.{1}.{2}.{3}" -f ([uint32]$numeric[0] + 1), $numeric[1], $numeric[2], $fourthPart)
    }

    return [pscustomobject]@{
        Canonical = $canonical
        PEVersion = ("{0}.{1}.{2}.{3}" -f $numeric[0], $numeric[1], $numeric[2], $fourthPart)
        MSIXVersion = $msixVersion
        NumericParts = [uint16[]]@($numeric[0], $numeric[1], $numeric[2], $fourthPart)
        IsPrerelease = -not [string]::IsNullOrWhiteSpace($prerelease)
        SpecialBuild = $specialBuild
    }
}
