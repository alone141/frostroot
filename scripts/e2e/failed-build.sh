#!/usr/bin/env bash
# proves: a build that asks for a package that does not exist fails with exit 2, prints apt's own explanation first, or dnf5's for Fedora, keeps its work directory, and writes neither a lock nor a tarball
# needs: the network, user namespaces, and mmdebstrap and ubuntu-keyring, or for Fedora mkosi with dnf, rpm, createrepo_c and bubblewrap
# takes: about 2 minutes
#
# The line that explains a failed build is found in mmdebstrap's whole log,
# or mkosi's, and apt's wording is apt's to change and dnf5's dnf5's, so
# only a real build shows that the search still finds it.
source "$(dirname "$0")/lib.sh"

e2e_begin failed-build
e2e_require_bootstrap
e2e_build_frostroot
lab=$LAB/lab
e2e_recipe "$lab" e2e-broken "$E2E_RELEASE" "ninja-buld"
program=mmdebstrap
explanation="Unable to locate package ninja-buld"
if [ "$E2E_DISTRO" = fedora ]; then
	program=mkosi
	explanation="No match for argument: ninja-buld"
fi

e2e_run build "$lab" "$FROSTROOT" build --plain
e2e_check "the build fails as a build failure, exit 2" test "$e2eStatus" -eq 2
e2e_check "it says $program failed" e2e_contains "$LAB/build.err" "frostroot: build failed: $program failed"
e2e_check "it points at the line of the log that explains it" e2e_contains "$LAB/build.err" "the first error in $program.log, line"
e2e_check "and that line is the package manager's" e2e_contains "$LAB/build.err" "$explanation"
e2e_check "it keeps the work directory to look at" e2e_contains "$LAB/build.err" "work directory kept at"
e2e_check "no lock was written" test ! -e "$lab/frostroot.lock"
e2e_check "no tarball was written" test ! -e "$lab/$(e2e_tarball e2e-broken)"
e2e_end
