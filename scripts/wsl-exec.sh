#!/usr/bin/env bash
# The Linux half of scripts/wsl.sh and scripts/wsl.ps1: run one command in
# the checkout with the environment a login shell gives, plus the Go tool
# directory, where golangci-lint and govulncheck are installed and which no
# profile puts on PATH.
#
# It exists so that the Windows side never has to pass a shell snippet to
# WSL. "wsl.exe -- ..." hands its arguments to the distribution's default
# shell, which expands every $VARIABLE in them before the command sees it:
# `wsl -- bash -lc 'X=1; echo $X'` prints an empty line. The wrappers use
# "wsl.exe -e bash -l scripts/wsl-exec.sh COMMAND...", which starts no outer
# shell, and this file does the rest with its arguments intact.
set -euo pipefail

if command -v go > /dev/null 2>&1; then
	PATH="$PATH:$(go env GOPATH)/bin"
	export PATH
fi
if [ "$#" -eq 0 ]; then
	echo "usage: scripts/wsl-exec.sh COMMAND [ARG...]" >&2
	exit 2
fi
exec "$@"
