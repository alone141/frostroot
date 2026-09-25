#!/usr/bin/env bash
# Run a command inside WSL, in this checkout, from Git Bash on Windows.
#
#   scripts/wsl.sh scripts/check.sh
#   scripts/wsl.sh go test -run TestBuild ./internal/cli/
#   scripts/wsl.sh scripts/e2e/run.sh pip-trust
#
# frostroot builds Linux images and its toolchain lives in WSL, so on a
# Windows checkout every go, lint or build command goes through here.
# FROSTROOT_WSL_DISTRO names the distribution; the default one otherwise.
#
# Two things this avoids, both of which broke hand-typed commands:
#   - "wsl.exe -- ..." runs through the distribution's default shell, which
#     expands $VARIABLES before your command sees them. This uses -e, which
#     starts no shell, and scripts/wsl-exec.sh sets up the environment.
#   - Git Bash rewrites arguments that look like POSIX paths (/var/tmp/x
#     becomes C:/Program Files/Git/var/tmp/x) when calling a Windows
#     program. MSYS_NO_PATHCONV turns that off.
#   - WSL passes no variable from the Windows side unless WSLENV names it,
#     so FROSTROOT_INTEGRATION_TIMEOUT=90m scripts/wsl.sh ... would reach
#     the script as unset. Every FROSTROOT_* and E2E_* variable set here,
#     and SOURCE_DATE_EPOCH, is added to WSLENV.
set -euo pipefail

if [ "$#" -eq 0 ]; then
	sed -n '2,10s/^# \{0,1\}//p' "$0"
	exit 2
fi
root=$(cd "$(dirname "$0")/.." && pwd -W 2> /dev/null || pwd)
distro=()
if [ -n "${FROSTROOT_WSL_DISTRO:-}" ]; then
	distro=(-d "$FROSTROOT_WSL_DISTRO")
fi
while IFS= read -r name; do
	WSLENV="${WSLENV:+$WSLENV:}$name"
done < <(compgen -e | grep -E '^(FROSTROOT_|E2E_)|^SOURCE_DATE_EPOCH$' || true)
export WSLENV="${WSLENV:-}"
export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'
exec wsl.exe "${distro[@]}" --cd "$root" -e bash -l scripts/wsl-exec.sh "$@"
