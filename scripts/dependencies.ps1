$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$dashboardDir = Join-Path $repoRoot 'dashboard'
$webDir = Join-Path $dashboardDir 'web'
$artifactDir = Join-Path $repoRoot 'artifacts'
$noticesDir = Join-Path $artifactDir 'notices'
$logDir = Join-Path $artifactDir 'logs'
New-Item -ItemType Directory -Force -Path $artifactDir, $noticesDir, $logDir | Out-Null

function Get-ToolPath {
    param([Parameter(Mandatory)][string]$Name)
    $command = Get-Command $Name -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($command) {
        if ($command.Source) { return $command.Source }
        return $command.Path
    }
    if ($Name -eq 'go.exe') {
        $knownGoPath = 'C:\Program Files\Go\bin\go.exe'
        if (Test-Path -LiteralPath $knownGoPath -PathType Leaf) { return $knownGoPath }
    }
    throw "$Name was not found."
}

function Invoke-Captured {
    param(
        [Parameter(Mandatory)][string]$Executable,
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$WorkingDirectory,
        [Parameter(Mandatory)][string]$OutputPath
    )
    $previousLocation = Get-Location
    $stderrPath = "$OutputPath.stderr.tmp"
    try {
        Set-Location -LiteralPath $WorkingDirectory
        & $Executable @Arguments 1> $OutputPath 2> $stderrPath
        $exitCode = $LASTEXITCODE
    }
    catch {
        $_ | Out-String | Set-Content -LiteralPath $OutputPath
        $exitCode = 1
    }
    finally {
        Set-Location -LiteralPath $previousLocation
    }
    if ($exitCode -ne 0) {
        if (Test-Path -LiteralPath $stderrPath) { Move-Item -LiteralPath $stderrPath -Destination (Join-Path $logDir (([System.IO.Path]::GetFileName($OutputPath)) + '.stderr.log')) -Force }
        throw "Command failed (exit $exitCode): $Executable $($Arguments -join ' ')"
    }
    Remove-Item -LiteralPath $stderrPath -ErrorAction SilentlyContinue
}

$npm = Get-ToolPath 'npm.cmd'
$go = Get-ToolPath 'go.exe'
$exePath = Join-Path $artifactDir 'Pokegents.exe'
if (-not (Test-Path -LiteralPath $exePath -PathType Leaf)) { throw "Executable not found: $exePath" }

# npm sbom writes its JSON document to stdout. Keep it in a temporary artifact until validated.
$npmSbomTemp = Join-Path $artifactDir 'sbom-npm.tmp'
$npmSbomPath = Join-Path $artifactDir 'sbom-npm.json'
Invoke-Captured -Executable $npm -Arguments @('sbom', '--sbom-format', 'cyclonedx') -WorkingDirectory $webDir -OutputPath $npmSbomTemp
try {
    $npmSbom = Get-Content -Raw -LiteralPath $npmSbomTemp | ConvertFrom-Json
    if (-not $npmSbom) { throw 'empty SBOM document' }
    Move-Item -LiteralPath $npmSbomTemp -Destination $npmSbomPath -Force
}
catch {
    Move-Item -LiteralPath $npmSbomTemp -Destination (Join-Path $logDir 'npm-sbom-failure.log') -Force
    throw "npm SBOM output was not valid JSON: $_"
}

$goBuildInfoPath = Join-Path $artifactDir 'go-build-info.txt'
$goModulesPath = Join-Path $artifactDir 'go-module-inventory.json'
Invoke-Captured -Executable $go -Arguments @('version', '-m', '../artifacts/Pokegents.exe') -WorkingDirectory $dashboardDir -OutputPath $goBuildInfoPath
Invoke-Captured -Executable $go -Arguments @('list', '-m', '-json=Path,Version,Sum,GoModSum,GoVersion,Indirect,Main', 'all') -WorkingDirectory $dashboardDir -OutputPath $goModulesPath
$goModuleCount = (Select-String -LiteralPath $goModulesPath -Pattern '^\s*"Path":' | Measure-Object).Count

$goDownloadLog = Join-Path $logDir 'go-mod-download.log'
Invoke-Captured -Executable $go -Arguments @('mod', 'download', 'all') -WorkingDirectory $dashboardDir -OutputPath $goDownloadLog
$goModuleDirsPath = Join-Path $logDir 'go-module-dirs.log'
Invoke-Captured -Executable $go -Arguments @('list', '-m', '-f', '{{.Path}}|{{.Version}}|{{.Dir}}', 'all') -WorkingDirectory $dashboardDir -OutputPath $goModuleDirsPath

# Inventory only installed npm packages and the Go module cache; never inspect credentials or general home files.
$inventory = [System.Collections.Generic.List[object]]::new()
$licensePattern = '(?i)^(license|licence|copying|notice)([-._].*)?$'
$nodeModules = Join-Path $webDir 'node_modules'
if (Test-Path -LiteralPath $nodeModules -PathType Container) {
    $nodeFiles = Get-ChildItem -LiteralPath $nodeModules -Recurse -File | Where-Object { $_.Name -match $licensePattern }
    foreach ($file in $nodeFiles) {
        $relative = [System.IO.Path]::GetRelativePath($nodeModules, $file.FullName).Replace('\', '/')
        $destination = Join-Path (Join-Path $noticesDir 'node_modules') $relative
        $copied = $false
        if (-not (Test-Path -LiteralPath $destination)) {
            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
            Copy-Item -LiteralPath $file.FullName -Destination $destination
            $copied = $true
        }
        $inventory.Add([pscustomobject]@{ source = "node_modules/$relative"; copy = "notices/node_modules/$relative"; copied = $copied })
    }
}

$goModCache = (& $go env GOMODCACHE).Trim()
$goModCacheRoot = [System.IO.Path]::GetFullPath($goModCache).TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
$goModCacheRootResolved = (Resolve-Path -LiteralPath $goModCache).Path.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
$mainModule = (Select-String -LiteralPath (Join-Path $dashboardDir 'go.mod') -Pattern '^module\s+(.+?)\s*$').Matches[0].Groups[1].Value
$goModuleLines = @(Get-Content -LiteralPath $goModuleDirsPath | Where-Object { $_ })
if ($goModuleLines.Count -eq 0) { throw 'Go module inventory is empty.' }
$goModuleSeen = 0
$missingGoLicenses = [System.Collections.Generic.List[string]]::new()
foreach ($line in $goModuleLines) {
    $fields = $line -split '\|', 3
    if ($fields.Count -ne 3 -or !$fields[0] -or !$fields[2]) { throw 'Go module directory output is malformed.' }
    $modulePath = $fields[0]
    $moduleDir = [System.IO.Path]::GetFullPath($fields[2])
    if ($modulePath -eq $mainModule) { continue }
    if (-not $moduleDir.StartsWith($goModCacheRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not (Test-Path -LiteralPath $moduleDir -PathType Container)) {
        throw "Go module directory is outside the canonical module cache: $modulePath"
    }
    $moduleDirResolved = (Resolve-Path -LiteralPath $moduleDir).Path.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
    if (-not $moduleDirResolved.StartsWith($goModCacheRootResolved, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Go module directory is outside the canonical module cache: $modulePath"
    }
    $goModuleSeen++
    $goFiles = @(Get-ChildItem -LiteralPath $moduleDir -Recurse -File | Where-Object { $_.Name -match $licensePattern })
    if ($goFiles.Count -eq 0) {
        $missingGoLicenses.Add($modulePath)
        continue
    }
    foreach ($file in $goFiles) {
        $relative = [System.IO.Path]::GetRelativePath($goModCache, $file.FullName).Replace('\', '/')
        $destination = Join-Path (Join-Path $noticesDir 'gomodcache') $relative
        $copied = $false
        if (-not (Test-Path -LiteralPath $destination)) {
            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
            Copy-Item -LiteralPath $file.FullName -Destination $destination
            $copied = $true
        }
        $inventory.Add([pscustomobject]@{ source = "gomodcache/$relative"; copy = "notices/gomodcache/$relative"; copied = $copied })
    }
}
if ($goModuleSeen -eq 0) { throw 'Go module inventory contains no external modules.' }
if ($missingGoLicenses.Count -gt 0) { throw ($missingGoLicenses -join ', ') }

$inventoryPath = Join-Path $artifactDir 'license-inventory.json'
if ($inventory.Count -eq 0) { throw 'License inventory is empty.' }
$inventory | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $inventoryPath -Encoding utf8
$npmComponents = 0
if ($npmSbom.components) { $npmComponents = @($npmSbom.components).Count }
$nodeCount = @($inventory | Where-Object { $_.source.StartsWith('node_modules/', [System.StringComparison]::OrdinalIgnoreCase) }).Count
$goLicenseCount = $inventory.Count - $nodeCount
Write-Host ("npm SBOM components={0}; Go modules={1}; license/notice files found npm={2}, Go={3}; inventory={4}" -f $npmComponents, $goModuleSeen, $nodeCount, $goLicenseCount, $inventoryPath)
