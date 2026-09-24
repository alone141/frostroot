#!/usr/bin/env bash
# The integration tests, run the way CI's integration job runs them: every
# package that has one, real mmdebstrap and apt, and two checks on the log
# that a green summary alone cannot give.
#
# usage: scripts/integration.sh [GO-TEST-ARGS...]
#
#   With no arguments every integration test runs (four real bootstraps and
#   the real index of every release: 10 to 15 minutes), and the run fails
#   unless each test CI requires was seen to pass. Arguments replace ./...,
#   e.g. -run TestIntegrationPython ./internal/builder/; a subset cannot
#   prove the list, so only the skip check applies then.
#
# Needs Linux, mmdebstrap, ubuntu-keyring, the network, and user namespaces
# or root. The log goes to $FROSTROOT_INTEGRATION_LOG, by default
# /var/tmp/frostroot-integration-<uid>.log.
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2

# Every name here must appear as a pass in a full run. The defect this list
# was made for was a test nobody ran, which is indistinguishable from a test
# that passed unless something insists on seeing it: CI once named
# ./internal/builder/ alone, and the integration tests of internal/form and
# internal/index had never run, though the README said they did. A test
# renamed or moved fails the run and is meant to, because the list is how the
# run knows what it is for.
requiredTests=(
	TestIntegrationNobleTiny
	TestIntegrationOfflineRebuild
	TestIntegrationPython
	TestIntegrationCatalogExistsInEveryRelease
	TestIntegrationOpenEveryRelease
)

# skipUnlessMmdebstrapAvailable skips when one of these is missing, and a
# skip reads exactly like a pass in the summary, so the prerequisites are
# checked here first and the log is searched for the skip messages after.
if ! command -v go > /dev/null 2>&1; then
	echo "scripts/integration.sh: go is not on PATH; on Windows run it through scripts/wsl.sh" >&2
	exit 2
fi
command -v mmdebstrap > /dev/null || {
	echo "mmdebstrap is not installed: sudo apt install mmdebstrap"
	exit 1
}
test -e /usr/share/keyrings/ubuntu-archive-keyring.gpg || {
	echo "the Ubuntu archive keyring is not installed: sudo apt install ubuntu-keyring"
	exit 1
}

log=${FROSTROOT_INTEGRATION_LOG:-/var/tmp/frostroot-integration-$(id -u).log}
testArgs=("$@")
full=0
if [ "${#testArgs[@]}" -eq 0 ]; then
	testArgs=(./...)
	full=1
fi

# No -race: these tests spend their time in mmdebstrap and apt, not in Go,
# and scripts/check.sh already runs the race detector over the same
# packages. The default 10m timeout is not enough for four real bootstraps.
go test -tags=integration -timeout 40m -v "${testArgs[@]}" 2>&1 | tee "$log"
status=${PIPESTATUS[0]}

# Only the three reasons skipUnlessMmdebstrapAvailable gives, not any skip:
# TestIntegrationUnreachableWorkRootFailsFast skips as root on purpose,
# because root mode has no user namespace to make unreachable, and that is a
# fact about the host rather than a run that did nothing.
skipReasons="needs Linux|mmdebstrap not installed|ubuntu-keyring not installed"
if grep -qE -- "$skipReasons" "$log"; then
	echo "The integration tests skipped for want of a prerequisite:"
	grep -E -- "$skipReasons" "$log"
	status=1
fi

if [ "$full" -eq 1 ]; then
	echo "=== every required integration test passed?"
	for name in "${requiredTests[@]}"; do
		if grep -qE -- "^--- PASS: $name " "$log"; then
			echo "ok        $name"
		else
			echo "MISSING   $name did not run to a pass"
			status=1
		fi
	done
fi
echo "log: $log"
exit "$status"
