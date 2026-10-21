# Windows end-to-end check, run by the CI workflow's windows job.
#
# Builds the release the way the site does, serves it on this machine, installs
# it with the one-line installer into a scratch profile, then runs every Claude
# Code hook the install registered the way Claude Code on Windows runs them:
# through Git Bash (https://code.claude.com/docs/en/hooks, "shell" field).
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$version = '0.0.0-ci'
$dirty = git status --porcelain
if ($dirty) {
    Write-Host "::warning::the working tree changed during the earlier steps:`n$dirty"
    go run ./packaging/release -version $version -allow-dirty
} else {
    go run ./packaging/release -version $version
}
if ($LASTEXITCODE -ne 0) { throw 'the release build failed' }

# A scratch profile, so the install sees a Claude Code with no other settings.
$profileDir = Join-Path $env:RUNNER_TEMP 'agent-brain-profile'
$env:USERPROFILE = $profileDir
$env:HOME = $profileDir
$env:APPDATA = Join-Path $profileDir 'AppData\Roaming'
$env:LOCALAPPDATA = Join-Path $profileDir 'AppData\Local'
New-Item -ItemType Directory -Force -Path (Join-Path $profileDir '.claude'), $env:APPDATA, $env:LOCALAPPDATA | Out-Null

$port = 8765
$base = "http://127.0.0.1:$port"
$server = Start-Process python -ArgumentList '-m', 'http.server', "$port", '--bind', '127.0.0.1', '--directory', 'dist/site' -PassThru -WindowStyle Hidden
try {
    $up = $false
    for ($i = 0; $i -lt 50 -and -not $up; $i++) {
        try { $null = Invoke-WebRequest "$base/releases/latest.json" -UseBasicParsing; $up = $true }
        catch { Start-Sleep -Milliseconds 200 }
    }
    if (-not $up) { throw "the release server on $base did not start" }

    $env:AGENT_BRAIN_BASE_URL = $base
    $env:AGENT_BRAIN_YES = '1'
    Invoke-RestMethod "$base/install.ps1" | Invoke-Expression
}
finally {
    Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
}

$exe = Join-Path $env:LOCALAPPDATA 'Programs\agent-brain\agent-brain.exe'
$reported = (& $exe --version) -join ' '
if ($reported -notmatch [regex]::Escape($version)) { throw "the installed binary reports '$reported', expected $version" }
Write-Host "installed: $reported"

& $exe status
if ($LASTEXITCODE -ne 0) { throw "agent-brain status exited with $LASTEXITCODE" }

$settingsPath = Join-Path $profileDir '.claude\settings.json'
if (-not (Test-Path -LiteralPath $settingsPath)) { throw 'agent-brain install wrote no Claude Code settings' }
$settings = Get-Content -Raw -LiteralPath $settingsPath | ConvertFrom-Json
$hooks = @()
foreach ($event in $settings.hooks.PSObject.Properties) {
    foreach ($group in $event.Value) {
        foreach ($hook in $group.hooks) {
            if ($hook.command -like '*agent-brain*') { $hooks += [pscustomobject]@{ Event = $event.Name; Command = $hook.command } }
        }
    }
}
if ($hooks.Count -eq 0) { throw 'agent-brain install registered no Claude Code hooks' }

$bash = Join-Path $env:ProgramFiles 'Git\bin\bash.exe'
$cwd = $PWD.Path -replace '\\', '/'
$failed = @()
foreach ($hook in $hooks) {
    $payload = @{ session_id = 'ci-windows'; cwd = $cwd; hook_event_name = $hook.Event; transcript_path = '' } | ConvertTo-Json -Compress
    Write-Host "$($hook.Event): $($hook.Command)"
    $payload | & $bash -c $hook.Command
    if ($LASTEXITCODE -ne 0) { $failed += "$($hook.Event) exited with $LASTEXITCODE" }
}
if ($failed) { throw "Claude Code hooks failed under Git Bash: $($failed -join '; ')" }
Write-Host "all $($hooks.Count) hooks ran under Git Bash"
