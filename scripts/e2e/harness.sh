#!/usr/bin/env bash
# proves: the harness fails when it should: a false check fails a scenario, a check whose evidence is missing fails instead of passing, a wrong exit status stops it, and nothing outside the e2e root is ever removed
# needs: nothing but bash
# takes: a few seconds
#
# Run it after changing lib.sh. A harness that reports PASS for a check
# that could not have failed is worse than none, so each case below is a
# tiny scenario in its own process, and its verdict is compared with the
# one it must reach.
source "$(dirname "$0")/lib.sh"

e2e_begin harness

# expect_exit WANT DESCRIPTION SCRIPT runs SCRIPT as a scenario of its own,
# with the library, and checks that it exits WANT.
expect_exit() {
	local want=$1 description=$2 script=$3 status
	FROSTROOT_E2E_ROOT=$LAB/inner bash -c "source '$e2eRepo/scripts/e2e/lib.sh'; e2e_begin inner; $script" \
		> "$LAB/inner.log" 2>&1
	status=$?
	e2e_check "$description: exit $want" test "$status" -eq "$want"
}

expect_exit 0 "a scenario whose checks hold passes" 'e2e_check "holds" true; e2e_end'
expect_exit 1 "a false check fails the scenario" 'e2e_check "does not hold" false; e2e_end'
expect_exit 1 "a lacks-check on a file never written fails" \
	'e2e_check "lacks" e2e_lacks "$LAB/never-written" "anything"; e2e_end'
expect_exit 1 "a tree-lacks-check on a directory never made fails" \
	'e2e_check "lacks" e2e_tree_lacks "$LAB/never-made" "anything"; e2e_end'
expect_exit 1 "checks against a tarball that cannot be listed fail" \
	'entries=$(e2e_entries "$LAB/no-such.tar.gz"); e2e_check "lacks" e2e_lacks "$entries" "anything"; e2e_end'
expect_exit 1 "a wrong exit status stops the scenario at once" \
	'e2e_run step "$LAB" false; e2e_expect_status 0 step; e2e_check "never reached" true; echo reached; e2e_end'
e2e_check "and nothing after it ran" e2e_lacks "$LAB/inner.log" "reached"
expect_exit 3 "an inconclusive scenario says so" 'e2e_inconclusive "the world moved"'
expect_exit 4 "a missing tool skips the scenario" 'e2e_require no-such-tool-anywhere; e2e_end'

# A directory outside the root, with something in it: if the guard ever
# broke, this is all it could remove.
outside=$(mktemp -d /var/tmp/frostroot-harness-outside.XXXXXX)
touch "$outside/still-here"
e2e_check "e2e_remove refuses a path outside the e2e root" e2e_not e2e_remove "$outside"
e2e_check "and leaves it where it was" test -e "$outside/still-here"
rm -rf "$outside"
e2e_end
