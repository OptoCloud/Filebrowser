#Requires -Version 7
<#
.SYNOPSIS
  Install or update filebrowser on Windows from GitHub releases.

.DESCRIPTION
  Verifies SHA256SUMS (and its SSH signature when release_signers exists in
  the config directory), backs up the database with pg_dump when available,
  swaps the binary, restarts the service, waits for the health check and
  rolls back automatically if it fails.

  Settings: $env:ProgramData\filebrowser\update.psd1 (Repo, GitHubToken).

.EXAMPLE
  update.ps1                 # update to the latest release if newer
  update.ps1 -Check          # report only (exit 10 if an update is available)
  update.ps1 -Version v1.2.3
  update.ps1 -From C:\dl     # install downloaded release files (offline)
  update.ps1 -Rollback
#>
[CmdletBinding()]
param(
    [switch]$Check,
    [string]$Version,
    [string]$From,
    [switch]$Rollback,
    [string]$InstallDir = (Join-Path $env:ProgramFiles 'filebrowser'),
    [string]$DataDir = (Join-Path $env:ProgramData 'filebrowser')
)
$ErrorActionPreference = 'Stop'

$Etc = $DataDir
$Prog = $InstallDir
$Exe = Join-Path $Prog 'filebrowser.exe'
$Prev = Join-Path $Prog 'previous\filebrowser.exe'
$Config = Join-Path $Etc 'filebrowser.toml'
$Service = 'filebrowser'
$Asset = 'filebrowser-windows-amd64.exe'

$settings = @{ Repo = 'optocloud/filebrowser'; GitHubToken = ''; PgDump = ''; DatabaseUrl = '' }
$settingsFile = Join-Path $Etc 'update.psd1'
if (Test-Path $settingsFile) { (Import-PowerShellDataFile $settingsFile).GetEnumerator() | ForEach-Object { $settings[$_.Key] = $_.Value } }

function Write-Log($msg) { Write-Host "filebrowser-update: $msg" }

function Invoke-Api($path, $accept = 'application/vnd.github+json', $outFile) {
    $h = @{ Accept = $accept; 'X-GitHub-Api-Version' = '2022-11-28' }
    if ($settings.GitHubToken) { $h.Authorization = "Bearer $($settings.GitHubToken)" }
    $uri = "https://api.github.com/repos/$($settings.Repo)/$path"
    try {
        if ($outFile) { Invoke-WebRequest -Uri $uri -Headers $h -OutFile $outFile -UseBasicParsing | Out-Null }
        else { Invoke-RestMethod -Uri $uri -Headers $h }
    } catch {
        throw "cannot read $path of $($settings.Repo) (private repository? set GitHubToken in $settingsFile): $($_.Exception.Message)"
    }
}

function Get-InstalledVersion { if (Test-Path $Exe) { (& $Exe version).Trim() } else { 'none' } }

function Test-Healthy {
    for ($i = 0; $i -lt 30; $i++) {
        & $Exe -config $Config health *> $null
        if ($LASTEXITCODE -eq 0) { return $true }
        Start-Sleep -Seconds 2
    }
    return $false
}

function Test-ServiceInstalled { [bool](Get-Service $Service -ErrorAction SilentlyContinue) }

function Switch-Binary($source) {
    if (Test-ServiceInstalled) { Stop-Service $Service -Force }
    Copy-Item $source $Exe -Force
    if (Test-ServiceInstalled) { Start-Service $Service }
}

function Confirm-Release($dir) {
    $sums = Join-Path $dir 'SHA256SUMS'
    $signers = Join-Path $Etc 'release_signers'
    if (Test-Path $signers) {
        $sig = "$sums.sig"
        if (-not (Test-Path $sig)) { throw "release is unsigned but $signers requires a signature" }
        Get-Content $sums -Raw | & ssh-keygen.exe -Y verify -f $signers -I filebrowser-release -n filebrowser-release -s $sig | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'SHA256SUMS signature is invalid' }
    }
    $pattern = "^([0-9a-f]{64}) [ *]$([regex]::Escape($Asset))$"
    $line = Get-Content $sums | Where-Object { $_ -match $pattern } | Select-Object -First 1
    if (-not $line -or $line -notmatch $pattern) { throw "SHA256SUMS has no entry for $Asset" }
    $want = $Matches[1]
    $got = (Get-FileHash (Join-Path $dir $Asset) -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($got -ne $want) { throw 'checksum mismatch' }
}

function Backup-Database($tag) {
    if (-not ($settings.PgDump -and $settings.DatabaseUrl) -or -not (Test-Path $Exe)) { return $null }
    $dir = Join-Path $Etc 'backups'
    New-Item -ItemType Directory -Force $dir | Out-Null
    $file = Join-Path $dir ("before-$tag-{0:yyyyMMdd-HHmmss}.dump" -f (Get-Date))
    & $settings.PgDump -Fc -f $file $settings.DatabaseUrl
    if ($LASTEXITCODE -ne 0) { throw 'database backup failed; not updating' }
    Get-ChildItem $dir -Filter 'before-*.dump' | Sort-Object LastWriteTime -Descending | Select-Object -Skip 5 | Remove-Item
    Write-Log "database backed up to $file"
    return $file
}

function Install-FromDir($src) {
    # Work on a private copy, so what is verified is what gets installed.
    $work = Join-Path ([IO.Path]::GetTempPath()) ("fb-update-" + [guid]::NewGuid())
    New-Item -ItemType Directory $work | Out-Null
    try {
        foreach ($f in $Asset, 'SHA256SUMS', 'SHA256SUMS.sig') {
            if (Test-Path (Join-Path $src $f)) { Copy-Item (Join-Path $src $f) $work }
        }
        Confirm-Release $work
        $new = Join-Path $work $Asset
        $ver = (& $new version).Trim()
        if ($ver -notmatch '^v[0-9A-Za-z.+-]+$') { throw "unexpected version string '$ver'" }
        if ($ver -eq (Get-InstalledVersion)) { Write-Log "$ver is already installed"; return }
        $backup = Backup-Database $ver
        New-Item -ItemType Directory -Force (Split-Path $Prev) | Out-Null
        $hadPrevious = Test-Path $Exe
        if ($hadPrevious) { Copy-Item $Exe $Prev -Force }
        Switch-Binary $new
        if ((Test-ServiceInstalled) -and -not (Test-Healthy)) {
            Write-Log "$ver failed its health check"
            if ($hadPrevious) {
                Write-Log 'rolling back'
                Switch-Binary $Prev
                if (Test-Healthy) { Write-Log 'rolled back' } else { Write-Log 'the previous version is not healthy either' }
                if ($backup) { Write-Log "if a migration ran, restore with: pg_restore --clean -d <database> $backup" }
            }
            exit 1
        }
        Write-Log "installed $ver"
    } finally {
        Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
    }
}

New-Item -ItemType Directory -Force $Prog | Out-Null

if ($Rollback) {
    if (-not (Test-Path $Prev)) { throw 'no previous version' }
    $tmp = Join-Path $Prog 'rollback.tmp'
    Copy-Item $Exe $tmp -Force
    Switch-Binary $Prev
    Move-Item $tmp $Prev -Force
    if ((Test-ServiceInstalled) -and -not (Test-Healthy)) { throw 'the previous version is not healthy' }
    Write-Log "rolled back to $(Get-InstalledVersion)"
    exit 0
}
if ($From) { Install-FromDir $From; exit 0 }

$rel = if ($Version) { Invoke-Api "releases/tags/$Version" } else { Invoke-Api 'releases/latest' }
$cur = Get-InstalledVersion
if ($Check) {
    if ($rel.tag_name -eq $cur) { Write-Log "up to date ($cur)"; exit 0 }
    Write-Log "update available: $cur -> $($rel.tag_name)"
    exit 10
}
if ($rel.tag_name -eq $cur -and -not $Version) { Write-Log "up to date ($cur)"; exit 0 }

$dl = Join-Path ([IO.Path]::GetTempPath()) ("fb-dl-" + [guid]::NewGuid())
New-Item -ItemType Directory $dl | Out-Null
try {
    Write-Log "downloading $($rel.tag_name)"
    foreach ($name in $Asset, 'SHA256SUMS', 'SHA256SUMS.sig') {
        $a = $rel.assets | Where-Object name -EQ $name
        if (-not $a) { if ($name -eq 'SHA256SUMS.sig') { continue }; throw "release has no $name" }
        Invoke-Api "releases/assets/$($a.id)" 'application/octet-stream' (Join-Path $dl $name)
    }
    Install-FromDir $dl
} finally {
    Remove-Item -Recurse -Force $dl -ErrorAction SilentlyContinue
}
