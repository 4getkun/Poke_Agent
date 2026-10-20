[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Version,
    [string]$IsccPath,
    [string]$ReleaseSignCommand
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ($Version -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
    throw 'Version must be a numeric MAJOR.MINOR.PATCH value, such as 1.2.3.'
}
foreach ($component in $Version.Split('.')) {
    if ([uint32]$component -gt 65535) {
        throw 'Each version component must be between 0 and 65535.'
    }
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$artifactDir = Join-Path $repoRoot 'artifacts'
$exePath = Join-Path $artifactDir 'Pokegents.exe'
$manifestPath = Join-Path $artifactDir 'build-manifest.json'
$noticesDir = Join-Path $artifactDir 'notices'
$licenseInventoryPath = Join-Path $artifactDir 'license-inventory.json'
$npmSbomPath = Join-Path $artifactDir 'sbom-npm.json'
$goModuleInventoryPath = Join-Path $artifactDir 'go-module-inventory.json'
$goBuildInfoPath = Join-Path $artifactDir 'go-build-info.txt'
$issPath = Join-Path $repoRoot 'installer\pokegents.iss'
$requiredFiles = @(
    $exePath,
    (Join-Path $repoRoot 'LICENSE'),
    (Join-Path $repoRoot 'THIRD_PARTY_NOTICES.md'),
    $manifestPath,
    $licenseInventoryPath,
    $npmSbomPath,
    $goModuleInventoryPath,
    $goBuildInfoPath,
    (Join-Path $repoRoot 'dashboard\web\src\assets\fonts\mplus1p\OFL.txt'),
    (Join-Path $repoRoot 'dashboard\web\src\assets\fonts\zenkakugothicnew\OFL.txt'),
    (Join-Path $repoRoot 'dashboard\web\src\assets\fonts\notosansjp\OFL.txt'),
    $issPath
)
foreach ($path in $requiredFiles) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Required packaging input is missing: $path"
    }
}
if (-not (Test-Path -LiteralPath $noticesDir -PathType Container) -or
    @(Get-ChildItem -LiteralPath $noticesDir -Recurse -File).Count -eq 0) {
    throw "Generated third-party notices are missing or empty: $noticesDir"
}
try {
    $licenseInventory = Get-Content -LiteralPath $licenseInventoryPath -Raw | ConvertFrom-Json
    $npmSbom = Get-Content -LiteralPath $npmSbomPath -Raw | ConvertFrom-Json
}
catch {
    throw 'Generated license inventory or npm SBOM is not valid JSON.'
}
if (-not $licenseInventory -or -not $npmSbom -or
    (Get-Item -LiteralPath $goModuleInventoryPath).Length -eq 0 -or
    (Get-Item -LiteralPath $goBuildInfoPath).Length -eq 0) {
    throw 'Generated license inventory inputs must be non-empty.'
}

try {
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
}
catch {
    throw "Build manifest is not valid JSON: $manifestPath"
}
if ($manifest.target -cne 'windows/amd64' -or
    $manifest.executable -cne 'Pokegents.exe' -or
    $manifest.database_assets_verification -cne 'passed' -or
    $manifest.app_version -cne $Version -or
    $manifest.sha256 -cnotmatch '^[0-9a-fA-F]{64}$') {
    throw 'Build manifest does not describe a verified Windows x64 Pokegents.exe with the requested version.'
}
$actualHash = (Get-FileHash -LiteralPath $exePath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actualHash -cne $manifest.sha256.ToLowerInvariant()) {
    throw 'Pokegents.exe SHA256 does not match artifacts/build-manifest.json.'
}

if ($IsccPath) {
    if (-not [System.IO.Path]::IsPathFullyQualified($IsccPath)) {
        throw 'IsccPath must be an absolute path to ISCC.exe.'
    }
    $compilerPath = $IsccPath
}
else {
    $compilerCandidates = [System.Collections.Generic.List[string]]::new()
    $programRoots = @(${env:ProgramFiles(x86)}, $env:ProgramFiles) | Where-Object { $_ } | Select-Object -Unique
    foreach ($root in $programRoots) {
        if (Test-Path -LiteralPath $root -PathType Container) {
            foreach ($installDir in Get-ChildItem -LiteralPath $root -Directory -Filter 'Inno Setup *' -ErrorAction SilentlyContinue) {
                $compilerCandidates.Add((Join-Path $installDir.FullName 'ISCC.exe'))
            }
        }
    }
    $compilerPath = $compilerCandidates |
        Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
        Select-Object -First 1
    if (-not $compilerPath) {
        throw 'Inno Setup compiler was not found in official Program Files installation paths; pass -IsccPath explicitly.'
    }
}
if (-not [System.IO.Path]::IsPathFullyQualified($compilerPath) -or
    [System.IO.Path]::GetFileName($compilerPath) -cne 'ISCC.exe' -or
    -not (Test-Path -LiteralPath $compilerPath -PathType Leaf)) {
    throw 'Compiler path must identify an existing ISCC.exe.'
}

$outputPath = Join-Path $artifactDir "Pokegents-Setup-$Version-x64.exe"
$compilerArgs = @(
    "/DMyAppVersion=$Version",
    "/DArtifactsDir=$artifactDir",
    "/DRepoRoot=$repoRoot",
    "/O$artifactDir",
    $issPath
)
if ($ReleaseSignCommand) {
    $compilerArgs = @('/DReleaseSigning=1', ('/Spokegents_release_sign=' + $ReleaseSignCommand)) + $compilerArgs
}
& $compilerPath @compilerArgs
$compilerExitCode = $LASTEXITCODE
if ($compilerExitCode -ne 0) {
    throw "Inno Setup compiler failed with exit code $compilerExitCode."
}
if (-not (Test-Path -LiteralPath $outputPath -PathType Leaf)) {
    throw "Compiler returned success but did not create the expected installer: $outputPath"
}
Write-Host "Package complete: $outputPath"
