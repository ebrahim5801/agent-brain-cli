<#
.SYNOPSIS
    agent-brain installer for Windows.

.DESCRIPTION
    Run from a clone of this repository. Checks the system requirements, builds
    the binary from source and installs it, then optionally wires up the AI
    assistants found on this machine.

.PARAMETER BinDir
    Install directory. Defaults to %LOCALAPPDATA%\Programs\agent-brain.

.PARAMETER Yes
    Do not prompt; integrate assistants automatically.

.PARAMETER NoIntegrate
    Install the binary only.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\install.ps1
#>

[CmdletBinding()]
param(
    [string]$BinDir = $env:AGENT_BRAIN_BIN_DIR,
    [switch]$Yes,
    [switch]$NoIntegrate
)

$ErrorActionPreference = 'Stop'

$ModulePath = 'github.com/ebrahim5801/agent-brain-cli'
$RepoDir = Split-Path -Parent $MyInvocation.MyCommand.Path

function Write-Ok   ($m) { Write-Host "[ok] $m" -ForegroundColor Green }
function Write-Warn ($m) { Write-Host "[!]  $m" -ForegroundColor Yellow }
function Die        ($m) { Write-Host "[x]  $m" -ForegroundColor Red; exit 1 }

function Test-VersionAtLeast([string]$Have, [string]$Want) {
    $h = ($Have -split '\.') + @('0', '0', '0')
    $w = ($Want -split '\.') + @('0', '0', '0')
    for ($i = 0; $i -lt 3; $i++) {
        $a = [int]$h[$i]; $b = [int]$w[$i]
        if ($a -lt $b) { return $false }
        if ($a -gt $b) { return $true }
    }
    return $true
}

# ---------------------------------------------------------------- requirements

Write-Host 'Checking system requirements'

$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -notin @('AMD64', 'ARM64')) {
    Die "unsupported architecture: $arch (amd64 and arm64 are supported)"
}
Write-Ok "Windows/$($arch.ToLower())"

$goMod = Join-Path $RepoDir 'go.mod'
if (-not (Test-Path $goMod)) {
    Die "go.mod not found in $RepoDir - run this script from a clone of the repository"
}
if (-not (Select-String -Path $goMod -Pattern "^module $([regex]::Escape($ModulePath))$" -Quiet)) {
    Die "$goMod is not the agent-brain module"
}
if (-not (Test-Path (Join-Path $RepoDir 'cmd\agent-brain'))) {
    Die "cmd\agent-brain not found in $RepoDir - the clone looks incomplete"
}
Write-Ok "repository at $RepoDir"

# The required toolchain is whatever go.mod asks for, so this never drifts.
$requiredGo = (Select-String -Path $goMod -Pattern '^go ([0-9][0-9.]*)').Matches[0].Groups[1].Value
if (-not $requiredGo) { Die 'could not read the go directive from go.mod' }

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Die "Go $requiredGo or newer is required but 'go' was not found. Install it from https://go.dev/dl/ and re-run."
}
$goVersion = (& go env GOVERSION) -replace '^go', ''
if (-not (Test-VersionAtLeast $goVersion $requiredGo)) {
    Die "Go $requiredGo or newer is required, found $goVersion. Upgrade from https://go.dev/dl/ and re-run."
}
Write-Ok "Go $goVersion"

$hasGit = [bool](Get-Command git -ErrorAction SilentlyContinue)
if ($hasGit) { Write-Ok ((& git --version) -replace 'version ', '') }
else { Write-Warn 'git not found - memory entries will be stored without git context' }

if (-not $BinDir) { $BinDir = Join-Path $env:LOCALAPPDATA 'Programs\agent-brain' }
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
Write-Ok "install directory $BinDir"

# ---------------------------------------------------------------------- build

Write-Host ''
Write-Host 'Building agent-brain'

$buildVersion = 'dev'
if ($hasGit) {
    $described = & git -C $RepoDir describe --tags --dirty 2>$null
    if ($LASTEXITCODE -eq 0 -and $described) { $buildVersion = $described.Trim() }
}

$target = Join-Path $BinDir 'agent-brain.exe'
$env:CGO_ENABLED = '0'
& go -C $RepoDir build -trimpath `
    -ldflags "-s -w -X $ModulePath/internal/version.Version=$buildVersion" `
    -o $target ./cmd/agent-brain
if ($LASTEXITCODE -ne 0) { Die 'build failed' }
Write-Ok "built $buildVersion"
Write-Ok "installed $target"

$paths = $env:PATH -split ';' | ForEach-Object { $_.TrimEnd('\') }
if ($paths -notcontains $BinDir.TrimEnd('\')) {
    Write-Warn "$BinDir is not on your PATH. Add it with:"
    Write-Warn "    setx PATH `"$BinDir;`$env:PATH`""
}

# ------------------------------------------------------------------ integrate

Write-Host ''
if ($NoIntegrate) {
    Write-Host "Skipping assistant integration. Run '$target install' when you are ready."
} elseif ($Yes) {
    & $target install
} else {
    Write-Host "'agent-brain install' detects the AI assistants on this machine and wires"
    Write-Host 'each one up. Existing settings are backed up first and no file inside any'
    Write-Host 'repository is touched.'
    $answer = Read-Host 'Run it now? [Y/n]'
    if ($answer -eq '' -or $answer -match '^(y|yes)$') { & $target install }
    else { Write-Host "Skipped. Run '$target install' when you are ready." }
}

Write-Host ''
Write-Host 'Done. Try: agent-brain status' -ForegroundColor Green
