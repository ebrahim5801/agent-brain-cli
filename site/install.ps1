# agent-brain installer for Windows.
#
#   irm https://agentbrain.wecansync.com/install.ps1 | iex
#
# Downloads the prebuilt binary for this CPU, checks it against the release's
# checksums.txt, installs it, adds its folder to your user PATH, then offers to
# run `agent-brain install` to integrate your coding assistants. No Go
# toolchain is needed. Works in Windows PowerShell 5.1 and PowerShell 7.
#
# Environment (set with $env:NAME = "value" before running):
#   AGENT_BRAIN_VERSION=0.1.0     install this version instead of the latest
#   AGENT_BRAIN_BIN_DIR=DIR       install directory (default %LOCALAPPDATA%\Programs\agent-brain)
#   AGENT_BRAIN_NO_INTEGRATE=1    install the binary only
#   AGENT_BRAIN_YES=1             integrate every detected assistant without asking
#   AGENT_BRAIN_BASE_URL=URL      download from a mirror

# Everything runs inside one function, so `irm | iex` never leaves variables
# behind and an interrupted download never runs half a script.
function Install-AgentBrain {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'
    # Windows PowerShell 5.1 may default to TLS 1.0; the release host needs 1.2.
    try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch { }

    $base = if ($env:AGENT_BRAIN_BASE_URL) { $env:AGENT_BRAIN_BASE_URL.TrimEnd('/') } else { 'https://agentbrain.wecansync.com' }
    $binDir = if ($env:AGENT_BRAIN_BIN_DIR) { $env:AGENT_BRAIN_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\agent-brain' }

    Write-Host 'Installing agent-brain' -ForegroundColor White

    $cpu = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    switch ($cpu) {
        'AMD64' { $arch = 'amd64' }
        'ARM64' { $arch = 'arm64' }
        default { throw "unsupported CPU: $cpu (amd64 and arm64 are supported)" }
    }
    Write-Host "  windows/$arch"

    $version = $env:AGENT_BRAIN_VERSION
    if (-not $version) {
        try { $version = (Invoke-RestMethod -UseBasicParsing -Uri "$base/releases/latest.json").version }
        catch { throw "could not read $base/releases/latest.json: $($_.Exception.Message)" }
    }
    $version = "$version".TrimStart('v')
    if ($version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$') { throw "not a release version: '$version'" }

    $archive = "agent-brain_windows_$arch.zip"
    $release = "$base/releases/$version"
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("agent-brain-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "Downloading agent-brain $version..."
        $zip = Join-Path $tmp $archive
        $sums = Join-Path $tmp 'checksums.txt'
        Invoke-WebRequest -UseBasicParsing -Uri "$release/$archive" -OutFile $zip
        Invoke-WebRequest -UseBasicParsing -Uri "$release/checksums.txt" -OutFile $sums

        $expected = $null
        foreach ($line in Get-Content -LiteralPath $sums) {
            $parts = $line -split '\s+', 2
            if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $archive) { $expected = $parts[0].ToLowerInvariant(); break }
        }
        if (-not $expected) { throw "checksums.txt has no entry for $archive" }
        $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash.ToLowerInvariant()
        if ($actual -ne $expected) { throw "checksum mismatch for $archive (expected $expected, got $actual)" }
        Write-Host '  checksum verified'

        $extract = Join-Path $tmp 'x'
        Expand-Archive -LiteralPath $zip -DestinationPath $extract -Force
        $exe = Join-Path $extract 'agent-brain.exe'
        if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw 'the archive has no agent-brain.exe' }
        # A binary for the wrong CPU throws rather than setting an exit code.
        $runs = $false
        try { $null = & $exe --version 2>&1; $runs = ($LASTEXITCODE -eq 0) } catch { }
        if (-not $runs) { throw 'the downloaded binary does not run on this machine' }

        New-Item -ItemType Directory -Force -Path $binDir | Out-Null
        $target = Join-Path $binDir 'agent-brain.exe'
        # A running agent-brain.exe cannot be overwritten, but it can be renamed
        # aside; the old copy is removed on the next install.
        Get-ChildItem -LiteralPath $binDir -Filter 'agent-brain.exe.old-*' -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
        if (Test-Path -LiteralPath $target) {
            try { Remove-Item -LiteralPath $target -Force }
            catch { Rename-Item -LiteralPath $target -NewName ('agent-brain.exe.old-' + [Guid]::NewGuid().ToString('N')) }
        }
        Copy-Item -LiteralPath $exe -Destination $target
        Write-Host "  installed $target ($(& $target --version 2>&1 | Select-Object -First 1))"
    }
    finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }

    # Add the folder to the user PATH (not the machine PATH) if it is missing.
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $entries = @($userPath -split ';' | Where-Object { $_ } | ForEach-Object { $_.TrimEnd('\') })
    if ($entries -notcontains $binDir.TrimEnd('\')) {
        $newPath = if ($userPath) { "$($userPath.TrimEnd(';'));$binDir" } else { $binDir }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Write-Host "  added $binDir to your user PATH (open a new terminal to use it)"
    }
    if (($env:Path -split ';' | ForEach-Object { $_.TrimEnd('\') }) -notcontains $binDir.TrimEnd('\')) {
        $env:Path = "$env:Path;$binDir"
    }

    Write-Host ''
    if ($env:AGENT_BRAIN_NO_INTEGRATE -eq '1') {
        Write-Host "Skipped assistant integration. Run 'agent-brain install' when you are ready."
    }
    elseif ($env:AGENT_BRAIN_YES -eq '1') {
        # With redirected input, `agent-brain install` integrates every detected
        # assistant without asking.
        $null | & $target install
    }
    elseif ([Environment]::UserInteractive -and -not [Console]::IsInputRedirected) {
        Write-Host "'agent-brain install' finds the AI assistants on this machine and asks"
        Write-Host 'before integrating each one. Existing settings are backed up first and'
        Write-Host 'no file inside any repository is touched.'
        $answer = Read-Host 'Run it now? [Y/n]'
        if ($answer -eq '' -or $answer -match '^(y|yes)$') { & $target install }
        else { Write-Host "Skipped. Run 'agent-brain install' when you are ready." }
    }
    else {
        Write-Host 'No interactive console, so assistant integration was skipped.'
        Write-Host "Run 'agent-brain install' to integrate your assistants."
    }

    Write-Host ''
    Write-Host 'Done. Next: agent-brain status' -ForegroundColor Green
}

Install-AgentBrain
