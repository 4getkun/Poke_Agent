[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Version,
    [Parameter(Mandatory)][string]$GoExecutable,
    [Parameter(Mandatory)][string]$RepoRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ($Version -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-dev)?$') {
    throw 'Version must be numeric MAJOR.MINOR.PATCH with an optional -dev suffix.'
}
$versionParts = @(
    foreach ($component in ($Version -replace '-dev$', '').Split('.')) {
        $number = [uint32]$component
        if ($number -gt 65535) {
            throw 'Each version component must be between 0 and 65535.'
        }
        [int]$number
    }
)
$numericVersion = '{0}.{1}.{2}.0' -f $versionParts[0], $versionParts[1], $versionParts[2]

if (-not (Test-Path -LiteralPath $GoExecutable -PathType Leaf)) {
    throw "Go executable was not found: $GoExecutable"
}
$repoRoot = (Resolve-Path -LiteralPath $RepoRoot).Path
$artifactDir = Join-Path $repoRoot 'artifacts'
$dashboardDir = Join-Path $repoRoot 'dashboard'
$jsonPath = Join-Path $artifactDir 'versioninfo.json'
$sysoPath = Join-Path $dashboardDir 'resource_windows_amd64.syso'
$statePath = Join-Path $artifactDir '.version-resource-state.json'
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null

$versionInfo = [ordered]@{
    FixedFileInfo = [ordered]@{
        FileVersion = [ordered]@{ Major = $versionParts[0]; Minor = $versionParts[1]; Patch = $versionParts[2]; Build = 0 }
        ProductVersion = [ordered]@{ Major = $versionParts[0]; Minor = $versionParts[1]; Patch = $versionParts[2]; Build = 0 }
        FileFlagsMask = '3f'
        FileFlags = '00'
        FileOS = '040004'
        FileType = '01'
        FileSubType = '00'
    }
    StringFileInfo = [ordered]@{
        Comments = ''
        CompanyName = '4getkun'
        FileDescription = 'Pokegents for Windows'
        FileVersion = $numericVersion
        InternalName = 'Pokegents'
        LegalCopyright = 'Copyright (c) 2026 Thariq Ridha; Windows contributors.'
        LegalTrademarks = ''
        OriginalFilename = 'Pokegents.exe'
        PrivateBuild = ''
        ProductName = 'Pokegents'
        ProductVersion = $numericVersion
        SpecialBuild = ''
    }
    VarFileInfo = [ordered]@{ Translation = [ordered]@{ LangID = '0409'; CharsetID = '04B0' } }
    IconPath = ''
    ManifestPath = ''
}
$json = $versionInfo | ConvertTo-Json -Depth 8
$jsonBytes = [System.Text.UTF8Encoding]::new($false).GetBytes($json + [Environment]::NewLine)

$jsonExisted = Test-Path -LiteralPath $jsonPath -PathType Leaf
$sysoExisted = Test-Path -LiteralPath $sysoPath -PathType Leaf
if ($jsonExisted -or $sysoExisted) {
    if (-not ($jsonExisted -and $sysoExisted -and (Test-Path -LiteralPath $statePath -PathType Leaf))) {
        throw 'Refusing to overwrite an existing version resource without matching generator state.'
    }
    $priorState = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
    $priorJsonHash = (Get-FileHash -LiteralPath $jsonPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $priorSysoHash = (Get-FileHash -LiteralPath $sysoPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($priorState.json_sha256 -ne $priorJsonHash -or $priorState.syso_sha256 -ne $priorSysoHash) {
        throw 'Refusing to overwrite a version resource that differs from its recorded generated files.'
    }
}

[System.IO.File]::WriteAllBytes($jsonPath, $jsonBytes)
Push-Location $repoRoot
try {
    $toolOutput = & $GoExecutable run 'github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0' -64 -o $sysoPath $jsonPath 2>&1
    $toolExitCode = $LASTEXITCODE
} finally {
    Pop-Location
}
if ($toolExitCode -ne 0 -or -not (Test-Path -LiteralPath $sysoPath -PathType Leaf)) {
    if (-not $jsonExisted) { Remove-Item -LiteralPath $jsonPath -Force -ErrorAction SilentlyContinue }
    if (-not $sysoExisted) { Remove-Item -LiteralPath $sysoPath -Force -ErrorAction SilentlyContinue }
    throw "goversioninfo failed to create the Windows version resource (exit code $toolExitCode): $($toolOutput -join ' ')"
}

$state = [ordered]@{
    json_sha256 = (Get-FileHash -LiteralPath $jsonPath -Algorithm SHA256).Hash.ToLowerInvariant()
    syso_sha256 = (Get-FileHash -LiteralPath $sysoPath -Algorithm SHA256).Hash.ToLowerInvariant()
}
$stateJson = $state | ConvertTo-Json
[System.IO.File]::WriteAllText($statePath, $stateJson + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
Write-Host "Windows version resource generated: $numericVersion"
