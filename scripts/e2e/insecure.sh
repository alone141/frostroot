#!/usr/bin/env bash
# proves: build --insecure says what it gives up and marks a Python resolve unverified in the lock, and the mark follows the lock: vendor and build --offline repeat it, and a verified vendor says what it checked
# needs: the network, mmdebstrap, ubuntu-keyring, user namespaces
# takes: about 25 minutes: an online build, vendor, an offline build
#
# Nothing here needs a network that intercepts TLS: what is checked is the
# flag's plumbing and its messages. That apt and pip really do skip
# verification under it is apt-trust's and pip-trust's to show, and that it
# leaves nothing in the image is no-build-leaks'.
source "$(dirname "$0")/lib.sh"

e2e_begin insecure
e2e_require_bootstrap
e2e_build_frostroot
lab=$LAB/lab
tarball=$lab/dist/e2e-insecure-ubuntu-24.04-amd64.tar.gz
e2e_recipe "$lab" e2e-insecure 24.04 "git" "requests"

e2e_run build "$lab" "$FROSTROOT" build --plain --insecure
e2e_expect_status 0 build
e2e_check "build warns that nothing is verified" e2e_contains "$LAB/build.err" "warning: --insecure: TLS certificates are not verified on this run"
e2e_check "that the .deb packages still are" e2e_contains "$LAB/build.err" "still checked against the archive's and each source's signatures"
e2e_check "and that the Python packages are not" e2e_contains "$LAB/build.err" "frostroot.lock will record it as unverified"
e2e_check "the lock marks the Python resolve unverified" e2e_contains "$lab/frostroot.lock" "transport = 'unverified'"
e2e_check_no_build_trust "$tarball"

e2e_run vendor "$lab" "$FROSTROOT" vendor --plain
e2e_expect_status 0 vendor
e2e_check "vendor repeats what the lock says" e2e_contains "$LAB/vendor.err" "resolved its Python packages without verifying TLS"
e2e_check "and says the wheels matched over a verified connection" e2e_contains "$LAB/vendor.out" "every wheel downloaded now matched the lock over a verified connection"

e2e_run offline "$lab" "$FROSTROOT" build --offline --plain
e2e_expect_status 0 offline
e2e_check "an offline rebuild repeats what the lock says" e2e_contains "$LAB/offline.err" "resolved its Python packages without verifying TLS"
e2e_check "and rebuilds what the lock names" e2e_contains "$LAB/offline.out" "every one as locked"
e2e_end
