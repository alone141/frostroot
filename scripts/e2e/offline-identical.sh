#!/usr/bin/env bash
# proves: two offline rebuilds of one lock are byte-identical, Python environment included, whatever PYTHON variables the build host has set, and leave the lock as it was
# needs: the network, mmdebstrap, ubuntu-keyring, user namespaces
# takes: about 30 minutes: an online build, vendor, two offline builds
#
# The promise the project is built on, end to end. A recipe with an apt
# package and a Python package is built online, vendored, and rebuilt
# offline twice, and the two tarballs must be the same bytes. The online
# build is not compared with them: it installs in another order than an
# offline rebuild does, which the README's "Rebuilding offline" explains.
# The second rebuild runs with PYTHONPYCACHEPREFIX set, as a build host
# might have it: the hook inherits the build user's environment, and that
# variable once sent every compiled cache to a directory named after the
# host, inside the image. E2E_RELEASE picks the Ubuntu release, 24.04 by
# default.
source "$(dirname "$0")/lib.sh"

e2e_begin offline-identical
e2e_require_bootstrap
e2e_build_frostroot
release=${E2E_RELEASE:-24.04}
lab=$LAB/lab
lock=$lab/frostroot.lock
tarball=$lab/dist/e2e-offline-ubuntu-$release-amd64.tar.gz
e2e_recipe "$lab" e2e-offline "$release" "git" "requests"

e2e_run build "$lab" "$FROSTROOT" build --plain
e2e_expect_status 0 build
e2e_check "the lock records the instant the image is frozen at" e2e_matches "$lock" '^source_date_epoch = [1-9]'
e2e_check "the lock records the Python wheels" e2e_contains "$lock" "[[pypi]]"
entries=$(e2e_entries "$tarball")
e2e_check "the image has git" e2e_has_line "$entries" "./usr/bin/git"
e2e_check "the image's environment has requests" e2e_matches "$entries" '^\./opt/frostroot/venv/lib/python3[.0-9]*/site-packages/requests/__init__\.py$'
lockBefore=$(e2e_sha "$lock")

e2e_run vendor "$lab" "$FROSTROOT" vendor --plain
e2e_expect_status 0 vendor

e2e_run offline1 "$lab" "$FROSTROOT" build --offline --plain
e2e_expect_status 0 offline1
e2e_check "the rebuild says it is reproducible" e2e_contains "$LAB/offline1.out" "byte for byte"
first=$(e2e_sha "$tarball")
mv "$tarball" "$LAB/offline1.tar.gz"

PYTHONPYCACHEPREFIX=$LAB/host-bytecode e2e_run offline2 "$lab" "$FROSTROOT" build --offline --plain
e2e_expect_status 0 offline2
second=$(e2e_sha "$tarball")
e2e_check "the second rebuild holds no cache written under the host's PYTHONPYCACHEPREFIX" e2e_lacks "$(e2e_entries "$tarball")" "host-bytecode"
echo "  offline 1: $first"
echo "  offline 2: $second"
e2e_check "two offline rebuilds are byte-identical" test "$first" = "$second"
e2e_check "the rebuilds left the lock as it was" test "$lockBefore" = "$(e2e_sha "$lock")"
e2e_end
