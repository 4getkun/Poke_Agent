[CmdletBinding()]
param(
    [string]$Version = '0.1.0-dev',
    [switch]$SkipInstall
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ($Version -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-dev)?$') {
    throw 'Version must be numeric MAJOR.MINOR.PATCH with an optional -dev suffix.'
}
foreach ($component in ($Version -replace '-dev$', '').Split('.')) {
    if ([uint32]$component -gt 65535) {
        throw 'Each version component must be between 0 and 65535.'
    }
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$dashboardDir = Join-Path $repoRoot 'dashboard'
$webDir = Join-Path $dashboardDir 'web'
$artifactDir = Join-Path $repoRoot 'artifacts'
$logDir = Join-Path $artifactDir 'logs'
New-Item -ItemType Directory -Force -Path $artifactDir, $logDir | Out-Null

function Get-ExternalPath {
    param([Parameter(Mandatory)][string]$Name)

    $command = Get-Command -Name $Name -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $command) {
        return $null
    }
    if ($command.Source) {
        return $command.Source
    }
    return $command.Path
}

function Invoke-LoggedCommand {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Executable,
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$WorkingDirectory,
        [Parameter(Mandatory)][string]$LogPath
    )

    Write-Host "[$Name] 開始"
    $previousLocation = Get-Location
    try {
        Set-Location -LiteralPath $WorkingDirectory
        & $Executable @Arguments *> $LogPath
        $exitCode = $LASTEXITCODE
    }
    catch {
        $_ | Out-String | Set-Content -LiteralPath $LogPath
        $exitCode = 1
    }
    finally {
        Set-Location -LiteralPath $previousLocation
    }

    Write-Host ("[{0}] 終了コード={1} ログ={2}" -f $Name, $exitCode, $LogPath)
    if ($exitCode -ne 0) {
        throw "$Name failed with exit code $exitCode; see $LogPath"
    }
}

function Get-VersionLine {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Executable,
        [string[]]$Arguments = @('--version')
    )

    $lines = @(& $Executable @Arguments 2>&1)
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0 -or $lines.Count -eq 0) {
        throw "Could not read $Name version (exit code $exitCode)."
    }
    return ([string]$lines[0]).Trim()
}

if ($env:OS -ne 'Windows_NT') {
    throw 'This build script requires Windows.'
}

$goPath = Get-ExternalPath 'go.exe'
if (-not $goPath) {
    $knownGoPath = 'C:\Program Files\Go\bin\go.exe'
    if (Test-Path -LiteralPath $knownGoPath -PathType Leaf) {
        $goPath = $knownGoPath
    }
}
if (-not $goPath) {
    throw 'go.exe was not found through Get-Command or C:\Program Files\Go\bin\go.exe.'
}

$nodePath = Get-ExternalPath 'node.exe'
$npmPath = Get-ExternalPath 'npm.cmd'
$gccPath = Get-ExternalPath 'gcc.exe'
if (-not $nodePath) { throw 'node.exe was not found through Get-Command.' }
if (-not $npmPath) { throw 'npm.cmd was not found through Get-Command.' }
if (-not $gccPath) { throw 'gcc.exe was not found through Get-Command.' }

$goVersion = Get-VersionLine 'Go' $goPath @('version')
$nodeVersion = Get-VersionLine 'Node' $nodePath
$npmVersion = Get-VersionLine 'npm' $npmPath
$compilerVersion = Get-VersionLine 'C compiler' $gccPath

if (-not $SkipInstall) {
    Invoke-LoggedCommand 'npm ci' $npmPath @('ci') $webDir (Join-Path $logDir 'build-npm-ci.log')
}
else {
    Write-Host '[npm ci] -SkipInstall により省略'
}

$tscPath = Join-Path $webDir 'node_modules\.bin\tsc.cmd'
$vitePath = Join-Path $webDir 'node_modules\.bin\vite.cmd'
if (-not (Test-Path -LiteralPath $tscPath -PathType Leaf)) {
    throw "TypeScript compiler not found: $tscPath"
}
if (-not (Test-Path -LiteralPath $vitePath -PathType Leaf)) {
    throw "Vite executable not found: $vitePath"
}

Invoke-LoggedCommand 'TypeScript' $tscPath @('--noEmit') $webDir (Join-Path $logDir 'build-web-tsc.log')
Invoke-LoggedCommand 'Windows Vite build' $vitePath @('build', '--config', 'vite.windows.config.ts') $webDir (Join-Path $logDir 'build-web-vite.log')
Invoke-LoggedCommand 'i18n check' $nodePath @('scripts/check-i18n.mjs') $webDir (Join-Path $logDir 'build-i18n.log')

$exePath = Join-Path $artifactDir 'Pokegents.exe'
$ldflags = "-s -w -H=windowsgui -X main.appVersion=$Version"
$versionResourceScript = Join-Path $PSScriptRoot 'version-resource.ps1'
& $versionResourceScript -Version $Version -GoExecutable $goPath -RepoRoot $repoRoot
if ($LASTEXITCODE -ne 0) {
    throw "Windows version resource generation failed (exit code $LASTEXITCODE)."
}
Invoke-LoggedCommand 'Go Windows build' $goPath @(
    'build',
    '-trimpath',
    '-tags', 'sqlite_fts5',
    '-ldflags', $ldflags,
    '-o', $exePath,
    '.'
) $dashboardDir (Join-Path $logDir 'desktop-build.log')
if (-not (Test-Path -LiteralPath $exePath -PathType Leaf)) {
    throw "Go build exited successfully but did not create $exePath."
}
$peInfo = (Get-Item -LiteralPath $exePath).VersionInfo
$peVersion = ($Version -replace '-dev$', '') + '.0'
if ($peInfo.ProductName -cne 'Pokegents' -or $peInfo.FileVersion -cne $peVersion -or
    $peInfo.ProductVersion -cne $peVersion -or $peInfo.OriginalFilename -cne 'Pokegents.exe') {
    throw 'Built executable PE identity does not match the requested product and version.'
}

# Help does not exercise CGo. Test SQLite in the exact executable before hashing.
$verifyProfile = Join-Path $artifactDir ('verify-profile-' + [guid]::NewGuid().ToString('N'))
$verifyStart = [System.Diagnostics.ProcessStartInfo]::new()
$verifyStart.FileName = $exePath
$verifyStart.UseShellExecute = $false
$verifyStart.CreateNoWindow = $true
$verifyStart.RedirectStandardOutput = $true
$verifyStart.RedirectStandardError = $true
$verifyStart.ArgumentList.Add('--verify')
$verifyStart.ArgumentList.Add('--data-dir')
$verifyStart.ArgumentList.Add($verifyProfile)
$verifyProcess = [System.Diagnostics.Process]::Start($verifyStart)
$verifyOutput = $verifyProcess.StandardOutput.ReadToEndAsync()
$verifyError = $verifyProcess.StandardError.ReadToEndAsync()
if (-not $verifyProcess.WaitForExit(15000)) {
    $verifyProcess.Kill()
    throw 'Built executable database verification timed out.'
}
$verifyCode = $verifyProcess.ExitCode
$verifyText = $verifyOutput.GetAwaiter().GetResult()
$verifyErrText = $verifyError.GetAwaiter().GetResult()
[System.IO.File]::WriteAllText((Join-Path $logDir 'desktop-database-smoke.log'), $verifyText + $verifyErrText)
$verifyProcess.Dispose()
if ($verifyCode -ne 0 -or $verifyText -notmatch '"database":"ok"') {
    throw "Built executable database verification failed: exit=$verifyCode"
}
Write-Host 'Executable database/assets verification: exit=0'
Invoke-LoggedCommand 'Go build metadata' $goPath @('version', '-m', '../artifacts/Pokegents.exe') $dashboardDir (Join-Path $artifactDir 'go-build-info.txt')
$exeHash = (Get-FileHash -LiteralPath $exePath -Algorithm SHA256).Hash.ToLowerInvariant()
$manifest = [ordered]@{
    schema_version = 1
    app_version = $Version
    generated_at_utc = (Get-Date).ToUniversalTime().ToString('o')
    target = 'windows/amd64'
    go_version = $goVersion
    node_version = $nodeVersion
    npm_version = $npmVersion
    c_compiler_version = $compilerVersion
    build_tags = @('sqlite_fts5')
    ldflags = $ldflags
    database_assets_verification = 'passed'
    executable = 'Pokegents.exe'
    sha256 = $exeHash
}
$manifestPath = Join-Path $artifactDir 'build-manifest.json'
$manifestJson = $manifest | ConvertTo-Json -Depth 4
[System.IO.File]::WriteAllText($manifestPath, $manifestJson + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))

Write-Host "Build complete: $exePath"
Write-Host "SHA256: $exeHash"
Write-Host "Manifest: $manifestPath"
