#!/usr/bin/env bash
# The checks CONTRIBUTING.md asks for before a push, as one command. CI runs
# its unit and vulnerability jobs through this script, so a clean run here is
# a clean run there. CI's lint job uses the golangci-lint action instead, at
# the version this script reads out of .github/workflows/ci.yml.
#
# usage: scripts/check.sh [--only STEPS] [--skip STEPS] [-- GO-TEST-ARGS...]
#
#   STEPS is a comma-separated list of: fmt vet test windows lint vuln
#   The default runs every step but vuln, which needs the network and can
#   turn red on any day a vulnerability is disclosed, so it runs when asked.
#   GO-TEST-ARGS replace ./... for the test step:
#     scripts/check.sh --only test -- -run TestInsecure ./internal/cli/
#
# Every step runs even when an earlier one fails, and the summary names the
# ones that failed. On a Windows checkout, run this through scripts/wsl.sh
# (Git Bash) or scripts/wsl.ps1 (PowerShell): the toolchain lives in WSL.
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2

allSteps=(fmt vet test windows lint vuln)
defaultSteps=(fmt vet test windows lint)
# Bump together with release.yml, which installs the same version.
govulncheckVersion=v1.8.0

usage() {
	sed -n '2,/^set /{/^#/s/^# \{0,1\}//p}' "$0"
}

selected=("${defaultSteps[@]}")
skipped=()
testArgs=()
while [ "$#" -gt 0 ]; do
	case "$1" in
	--only | --skip)
		if [ "$#" -lt 2 ]; then
			usage >&2
			exit 2
		fi
		if [ "$1" = --only ]; then
			IFS=, read -r -a selected <<< "$2"
		else
			IFS=, read -r -a skipped <<< "$2"
		fi
		shift 2
		;;
	--only=*) IFS=, read -r -a selected <<< "${1#--only=}" && shift ;;
	--skip=*) IFS=, read -r -a skipped <<< "${1#--skip=}" && shift ;;
	-h | --help)
		usage
		exit 0
		;;
	--)
		shift
		testArgs=("$@")
		break
		;;
	*)
		echo "scripts/check.sh: unknown argument \"$1\"; go test arguments go after --" >&2
		exit 2
		;;
	esac
done

contains() { # WORD LIST...
	local word=$1 item
	shift
	for item in "$@"; do
		[ "$item" = "$word" ] && return 0
	done
	return 1
}

for step in "${selected[@]}" "${skipped[@]}"; do
	if ! contains "$step" "${allSteps[@]}"; then
		echo "scripts/check.sh: no step \"$step\"; the steps are: ${allSteps[*]}" >&2
		exit 2
	fi
done

if ! command -v go > /dev/null 2>&1; then
	echo "scripts/check.sh: go is not on PATH." >&2
	case "$(uname -s)" in
	MINGW* | MSYS* | CYGWIN*) echo "On Windows the toolchain is in WSL: scripts/wsl.sh scripts/check.sh" >&2 ;;
	esac
	exit 2
fi
# go install puts golangci-lint and govulncheck here, and no profile adds it.
PATH="$PATH:$(go env GOPATH)/bin"

begin() {
	if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::group::$1"; else echo "=== $1"; fi
}

end() {
	if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::endgroup::"; fi
}

step_fmt() {
	# The files git would commit, rather than ".": a checkout can hold other
	# checkouts (.claude/worktrees here), and gofmt would walk into them.
	local files=() file
	if git rev-parse --git-dir > /dev/null 2>&1; then
		while IFS= read -r file; do
			[ -f "$file" ] && files+=("$file")
		done < <(git ls-files --cached --others --exclude-standard -- '*.go')
	else
		while IFS= read -r file; do
			files+=("$file")
		done < <(find . -name '*.go' -not -path './.*')
	fi
	local unformatted
	unformatted=$(gofmt -l "${files[@]}") || return 1
	if [ -n "$unformatted" ]; then
		echo "These files need gofmt -w:"
		echo "$unformatted"
		return 1
	fi
	echo "${#files[@]} files formatted"
}

step_vet() {
	# The integration tests compile only with their tag; vet both ways so a
	# change that breaks them is caught without running them.
	go vet ./... && go vet -tags=integration ./...
}

step_test() {
	if [ "${#testArgs[@]}" -eq 0 ]; then
		go test -race ./...
	else
		go test -race "${testArgs[@]}"
	fi
}

step_windows() {
	# The module must still compile off Linux: the CLI explains on Windows
	# that the build needs WSL, which it cannot do if it does not compile.
	GOOS=windows go build ./...
}

step_lint() {
	local want have
	want=$(sed -n 's/^ *version: v\([0-9][0-9.]*\)$/\1/p' .github/workflows/ci.yml | head -1)
	if ! command -v golangci-lint > /dev/null 2>&1; then
		echo "golangci-lint is not installed; CI uses v$want:"
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$want"
		return 1
	fi
	have=$(golangci-lint version 2> /dev/null | sed -n 's/.*version \([0-9][0-9.]*\).*/\1/p' | head -1)
	if [ -n "$want" ] && [ "$have" != "$want" ]; then
		echo "note: golangci-lint $have here and v$want in CI, so the findings may differ"
	fi
	golangci-lint run ./...
}

step_vuln() {
	# The toolchain decides which standard-library vulnerabilities are
	# reachable, so this is only meaningful on the toolchain go.mod pins,
	# which is the one CI and the release build with. When go.mod said
	# 1.24.2, govulncheck reported 32 reachable vulnerabilities in the
	# packages this project's security rests on (crypto/x509, crypto/tls,
	# net/http) and nothing was looking. GOTOOLCHAIN makes go use the pinned
	# toolchain, downloading it if this host has another.
	local pinned running
	pinned=$(sed -n 's/^go \([0-9.]*\)$/\1/p' go.mod)
	running=$(GOTOOLCHAIN="go$pinned" go env GOVERSION) || return 1
	echo "go.mod pins go$pinned; running $running"
	if [ "go$pinned" != "$running" ]; then
		echo "the toolchain is not the pinned one"
		return 1
	fi
	GOTOOLCHAIN="go$pinned" go run "golang.org/x/vuln/cmd/govulncheck@$govulncheckVersion" ./...
}

ran=()
failed=()
for step in "${allSteps[@]}"; do
	contains "$step" "${selected[@]}" || continue
	if [ "${#skipped[@]}" -gt 0 ] && contains "$step" "${skipped[@]}"; then
		continue
	fi
	begin "$step"
	started=$(date +%s)
	if "step_$step"; then
		result=ok
	else
		result=FAILED
		failed+=("$step")
	fi
	end
	ran+=("$step")
	echo "--- $step: $result ($(($(date +%s) - started)) s)"
done

if [ "${#ran[@]}" -eq 0 ]; then
	echo "scripts/check.sh: nothing to run" >&2
	exit 2
fi
if [ "${#failed[@]}" -gt 0 ]; then
	echo "FAILED: ${failed[*]} (of: ${ran[*]})"
	if [ -n "${GITHUB_ACTIONS:-}" ]; then
		for step in "${failed[@]}"; do
			echo "::error title=scripts/check.sh::the $step step failed"
		done
	fi
	exit 1
fi
echo "ok: ${ran[*]}"
