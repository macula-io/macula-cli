# Installs macula-cli for Windows: downloads the release archive matching
# this machine's architecture from GitHub Releases, verifies it against
# the release's own checksums.txt, and installs the binary.
#
# Usage (PowerShell):
#   irm https://raw.githubusercontent.com/macula-io/macula-cli/master/install.ps1 | iex
#
# Env overrides:
#   $env:MACULA_CLI_VERSION      pin a version (e.g. "v0.1.0") instead of latest
#   $env:MACULA_CLI_INSTALL_DIR  install directory (default: $env:LOCALAPPDATA\macula-cli)

$ErrorActionPreference = "Stop"

$Repo = "macula-io/macula-cli"
$InstallDir = if ($env:MACULA_CLI_INSTALL_DIR) { $env:MACULA_CLI_INSTALL_DIR } else { "$env:LOCALAPPDATA\macula-cli" }

function Get-Arch {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture
    switch ($arch) {
        "X64" { return "amd64" }
        "Arm64" { return "arm64" }
        default { throw "unsupported architecture: $arch" }
    }
}

$arch = Get-Arch

$version = $env:MACULA_CLI_VERSION
if (-not $version) {
    Write-Host "resolving latest release..."
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
    $version = $release.tag_name
    if (-not $version) { throw "could not resolve the latest release tag from the GitHub API" }
}

$versionNum = $version.TrimStart("v")
$archive = "macula-cli_${versionNum}_windows_${arch}.zip"
$baseUrl = "https://github.com/$Repo/releases/download/$version"

$workDir = New-Item -ItemType Directory -Path (Join-Path $env:TEMP "macula-cli-install-$(New-Guid)")
try {
    $archivePath = Join-Path $workDir $archive
    Write-Host "downloading $archive ($version)..."
    try {
        Invoke-WebRequest -Uri "$baseUrl/$archive" -OutFile $archivePath
    } catch {
        throw "download failed -- does release $version have a windows/$arch build? ($_)"
    }

    Write-Host "verifying checksum..."
    $checksumsPath = Join-Path $workDir "checksums.txt"
    Invoke-WebRequest -Uri "$baseUrl/checksums.txt" -OutFile $checksumsPath
    $expectedLine = Select-String -Path $checksumsPath -Pattern ([regex]::Escape($archive)) | Select-Object -First 1
    if (-not $expectedLine) { throw "no checksum entry found for $archive in checksums.txt" }
    $expected = ($expectedLine.Line -split '\s+')[0]
    $actual = (Get-FileHash -Path $archivePath -Algorithm SHA256).Hash.ToLower()
    if ($expected -ne $actual) {
        throw "checksum mismatch for ${archive}: expected $expected, got $actual"
    }

    Write-Host "extracting..."
    Expand-Archive -Path $archivePath -DestinationPath $workDir -Force

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -Path (Join-Path $workDir "macula-cli.exe") -Destination (Join-Path $InstallDir "macula-cli.exe") -Force

    Write-Host "installed macula-cli $version to $InstallDir\macula-cli.exe"

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath -notlike "*$InstallDir*") {
        Write-Host ""
        Write-Host "$InstallDir is not on your PATH. Add it for future sessions with:"
        Write-Host "  [Environment]::SetEnvironmentVariable('Path', `"`$([Environment]::GetEnvironmentVariable('Path','User'));$InstallDir`", 'User')"
        Write-Host "(then restart your terminal), or just run it directly this session:"
        $env:Path = "$InstallDir;$env:Path"
    }

    & (Join-Path $InstallDir "macula-cli.exe") --version
} finally {
    Remove-Item -Recurse -Force $workDir -ErrorAction SilentlyContinue
}
