# Uninstalls macula-cli: removes the binary install.ps1 placed. Leaves the
# persisted identity ($env:AppData\macula-cli\identity.key -- Go's
# os.UserConfigDir() resolves to %AppData%, NOT %LOCALAPPDATA% where the
# binary itself lives, a real distinction caught writing this script) alone
# by default -- it took real puzzle-grinding work to generate and
# reinstalling later should be able to reuse it; pass -Purge to remove it
# too.
#
# Usage:
#   irm https://raw.githubusercontent.com/macula-io/macula-cli/master/uninstall.ps1 | iex
#   # or, to pass -Purge, download first:
#   iwr -useb https://raw.githubusercontent.com/macula-io/macula-cli/master/uninstall.ps1 -OutFile uninstall.ps1
#   .\uninstall.ps1 -Purge
#
# Env overrides:
#   $env:MACULA_CLI_INSTALL_DIR  where to look for the binary (default: $env:LOCALAPPDATA\macula-cli)

param(
    [switch]$Purge
)

$ErrorActionPreference = "Stop"

$InstallDir = if ($env:MACULA_CLI_INSTALL_DIR) { $env:MACULA_CLI_INSTALL_DIR } else { "$env:LOCALAPPDATA\macula-cli" }
$BinPath = Join-Path $InstallDir "macula-cli.exe"
# The binary lives under %LOCALAPPDATA% (install.ps1's choice), but the
# running program persists its identity via Go's os.UserConfigDir(),
# which resolves to %AppData% (roaming) on Windows -- a different
# directory, not a subfolder of $InstallDir.
# The directory holds identity.key (macula 12), and identity.seed if a 10.x
# release ran here; -Purge removes it whole, as uninstall.sh does.
$IdentityDir = "$env:AppData\macula-cli"

if (Test-Path $BinPath) {
    Remove-Item -Force $BinPath
    Write-Host "removed $BinPath"
} else {
    Write-Host "no binary found at $BinPath (already removed, or installed elsewhere -- set `$env:MACULA_CLI_INSTALL_DIR)"
}

if ($Purge) {
    if (Test-Path $IdentityDir) {
        Remove-Item -Recurse -Force $IdentityDir
        Write-Host "removed $IdentityDir (-Purge: persisted identity deleted too)"
    } else {
        Write-Host "no identity directory found at $IdentityDir"
    }
} elseif (Test-Path $IdentityDir) {
    Write-Host "left $IdentityDir in place (persisted identity) -- pass -Purge to remove it too"
}
