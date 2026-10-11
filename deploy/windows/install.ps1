#Requires -Version 7
#Requires -RunAsAdministrator
<#
.SYNOPSIS
  Install filebrowser as a Windows service.

.DESCRIPTION
  Prerequisites: PostgreSQL 17 (winget install PostgreSQL.PostgreSQL.17) with
  a "filebrowser" role and database (deploy\postgres.sql), and ffmpeg 6.0 or
  newer. Put ffmpeg under Program Files (for example C:\Program Files\ffmpeg\bin),
  which the ffmpeg sandbox (an AppContainer) can read by default.

  Layout: binary in C:\Program Files\filebrowser, configuration, data, cache
  and backups in C:\ProgramData\filebrowser. The service runs as the virtual
  account "NT SERVICE\filebrowser", which only gets access to those folders
  and to FilesRoot. Safe to re-run.

.EXAMPLE
  .\install.ps1 -AppOrigin https://files.example.com -ShareOrigin https://share.example.com `
    -FilesRoot D:\Share -DatabaseUrl 'postgres://filebrowser:...@localhost/filebrowser' `
    -FFmpegDir 'C:\Program Files\ffmpeg\bin' [-From C:\Downloads\filebrowser-v1.2.3]
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$AppOrigin,
    [Parameter(Mandatory)][string]$ShareOrigin,
    [Parameter(Mandatory)][string]$FilesRoot,
    [Parameter(Mandatory)][string]$DatabaseUrl,
    [Parameter(Mandatory)][string]$FFmpegDir,
    [string]$From,
    [string]$GitHubToken = ''
)
$ErrorActionPreference = 'Stop'
$Etc = Join-Path $env:ProgramData 'filebrowser'
$Prog = Join-Path $env:ProgramFiles 'filebrowser'
$Exe = Join-Path $Prog 'filebrowser.exe'
$Config = Join-Path $Etc 'filebrowser.toml'
$Account = 'NT SERVICE\filebrowser'

foreach ($tool in 'ffmpeg.exe', 'ffprobe.exe') {
    if (-not (Test-Path (Join-Path $FFmpegDir $tool))) { throw "$tool not found in $FFmpegDir" }
}
New-Item -ItemType Directory -Force $Prog, $Etc, (Join-Path $Etc 'data'), (Join-Path $Etc 'cache'), (Join-Path $Etc 'backups'), $FilesRoot | Out-Null

# Configuration and settings are readable only by administrators, SYSTEM and
# (for the config) the service.
icacls $Etc /inheritance:r /grant:r 'Administrators:(OI)(CI)F' 'SYSTEM:(OI)(CI)F' | Out-Null
$settingsFile = Join-Path $Etc 'update.psd1'
if (-not (Test-Path $settingsFile)) {
    $pgDump = Get-ChildItem 'C:\Program Files\PostgreSQL\*\bin\pg_dump.exe' -ErrorAction SilentlyContinue |
        Sort-Object FullName -Descending | Select-Object -First 1
    @"
@{
    Repo        = 'optocloud/filebrowser'
    # Read-only token (fine-grained, "Contents: read") while the repository is private.
    GitHubToken = '$GitHubToken'
    # Used to back up the database before each update.
    PgDump      = '$($pgDump.FullName)'
    DatabaseUrl = '$DatabaseUrl'
}
"@ | Set-Content -Encoding utf8 $settingsFile
}
Copy-Item (Join-Path $PSScriptRoot 'update.ps1') $Prog -Force

# Binary.
$update = Join-Path $Prog 'update.ps1'
if ($From) { & $update -From $From } else { & $update }
if ($LASTEXITCODE) { throw 'installing the binary failed' }

# Configuration.
if (-not (Test-Path $Config)) {
    $fwd = { param($p) $p -replace '\\', '/' }
    $toml = (& $Exe config-example) -join "`n"
    $toml = $toml -replace '(?m)^listen = .*', 'listen = "127.0.0.1:8080"' `
        -replace '(?m)^app_origin = .*', "app_origin = `"$AppOrigin`"" `
        -replace '(?m)^share_origin = .*', "share_origin = `"$ShareOrigin`"" `
        -replace '(?m)^database_url = .*', "database_url = `"$DatabaseUrl`"" `
        -replace '(?m)^files_root = .*', "files_root = `"$(& $fwd $FilesRoot)`"" `
        -replace '(?m)^data_dir = .*', "data_dir = `"$(& $fwd (Join-Path $Etc 'data'))`"" `
        -replace '(?m)^cache_dir = .*', "cache_dir = `"$(& $fwd (Join-Path $Etc 'cache'))`"" `
        -replace '(?m)^ffmpeg = .*', "ffmpeg = `"$(& $fwd (Join-Path $FFmpegDir 'ffmpeg.exe'))`"" `
        -replace '(?m)^ffprobe = .*', "ffprobe = `"$(& $fwd (Join-Path $FFmpegDir 'ffprobe.exe'))`"" `
        -replace '(?m)^sandbox = .*', 'sandbox = "appcontainer"'
    Set-Content -Encoding utf8 -NoNewline $Config $toml
}

# Service and permissions.
if (-not (Get-Service filebrowser -ErrorAction SilentlyContinue)) {
    & $Exe -config $Config service install | Out-Null
    if ($LASTEXITCODE) { throw 'service install failed' }
}
icacls $Config /grant "${Account}:R" | Out-Null
foreach ($d in (Join-Path $Etc 'data'), (Join-Path $Etc 'cache'), $FilesRoot) {
    icacls $d /grant "${Account}:(OI)(CI)M" | Out-Null
}
# The ffmpeg sandbox (AppContainer) must be able to load ffmpeg.
icacls (Join-Path $FFmpegDir 'ffmpeg.exe') /grant '*S-1-15-2-1:RX' | Out-Null
icacls (Join-Path $FFmpegDir 'ffprobe.exe') /grant '*S-1-15-2-1:RX' | Out-Null

Restart-Service filebrowser
for ($i = 0; $i -lt 30; $i++) {
    & $Exe -config $Config health *> $null
    if ($LASTEXITCODE -eq 0) { break }
    Start-Sleep -Seconds 2
}
if ($LASTEXITCODE -ne 0) { throw 'filebrowser did not become healthy; see Event Viewer > Windows Logs > Application (source "filebrowser")' }
Write-Host "filebrowser $(& $Exe version) is running as a service."
Write-Host "Create the first admin:  & '$Exe' -config '$Config' user add admin -admin"
Write-Host "Update later with:       & '$update'"
