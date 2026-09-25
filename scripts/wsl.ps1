# Run a command inside WSL, in this checkout, from PowerShell on Windows.
#
#   .\scripts\wsl.ps1 scripts/check.sh
#   .\scripts\wsl.ps1 go test -run TestBuild ./internal/cli/
#   .\scripts\wsl.ps1 scripts/e2e/run.sh pip-trust
#
# frostroot builds Linux images and its toolchain lives in WSL, so on a
# Windows checkout every go, lint or build command goes through here.
# $env:FROSTROOT_WSL_DISTRO names the distribution; the default one otherwise.
#
# "wsl.exe -- ..." would run through the distribution's default shell, which
# expands $VARIABLES before the command sees them. This uses -e, which starts
# no shell, and scripts/wsl-exec.sh sets up the environment. Arguments keep
# their spaces, $, | and ' intact. Their own double quotes do not: Windows
# PowerShell 5.1 does not escape them for a native program, so 'say "hi"'
# arrives as: say hi. Put a command that needs them in a script file.
#
# WSL passes no variable from the Windows side unless WSLENV names it, so a
# $env:FROSTROOT_INTEGRATION_TIMEOUT set here would reach the script as
# unset. Every FROSTROOT_* and E2E_* variable set here, and
# SOURCE_DATE_EPOCH, is added to WSLENV for the call, and WSLENV is put back
# afterwards.
#
# If script execution is disabled on this machine, run it as:
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts\wsl.ps1 COMMAND...
$ErrorActionPreference = 'Stop'

if ($args.Count -eq 0) {
    Get-Content $PSCommandPath -TotalCount 9 | ForEach-Object { $_ -replace '^# ?', '' }
    exit 2
}
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$wslArgs = @()
if ($env:FROSTROOT_WSL_DISTRO) {
    $wslArgs += @('-d', $env:FROSTROOT_WSL_DISTRO)
}
$wslArgs += @('--cd', $root, '-e', 'bash', '-l', 'scripts/wsl-exec.sh')
$forwarded = Get-ChildItem Env: |
    Where-Object { $_.Name -match '^(FROSTROOT_|E2E_)' -or $_.Name -eq 'SOURCE_DATE_EPOCH' } |
    ForEach-Object { $_.Name }
$savedWslEnv = $env:WSLENV
try {
    if ($forwarded) {
        $env:WSLENV = (@($savedWslEnv) + @($forwarded) | Where-Object { $_ }) -join ':'
    }
    & wsl.exe @wslArgs @args
    $status = $LASTEXITCODE
} finally {
    $env:WSLENV = $savedWslEnv
}
exit $status
