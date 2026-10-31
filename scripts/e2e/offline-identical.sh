#!/usr/bin/env bash
# proves: two offline rebuilds of one lock are byte-identical, Python environment included, whatever PYTHON or MKOSI variables the build host has set, and leave the lock as it was; for Ubuntu, Fedora with E2E_DISTRO=fedora, or a disk image with E2E_TARGET=disk
# needs: the network, user namespaces, and mmdebstrap and ubuntu-keyring, or for Fedora mkosi with dnf, rpm, createrepo_c and bubblewrap; for a disk, bubblewrap and e2fsprogs, util-linux and dosfstools on the host
# takes: about 30 minutes for Ubuntu, 5 for Fedora, 45 for a disk: an online build, vendor, two offline builds
#
# The promise the project is built on, end to end. A recipe with an apt
# package and a Python package is built online, vendored, and rebuilt
# offline twice, and the two tarballs must be the same bytes. The online
# build is not compared with them: it installs in another order than an
# offline rebuild does, which the README's "Rebuilding offline" explains.
# The second rebuild runs with PYTHONPYCACHEPREFIX set, as a build host
# might have it: the hook inherits the build user's environment, and that
# variable once sent every compiled cache to a directory named after the
# host, inside the image. A Fedora recipe has no Python packages yet, and
# its second rebuild also runs with MKOSI_DNF=dnf, which, if it reached
# mkosi, would install with a dnf the tools tree does not have.
source "$(dirname "$0")/lib.sh"

e2e_begin offline-identical
e2e_require_bootstrap
e2e_build_frostroot
lab=$LAB/lab
lock=$lab/frostroot.lock
tarball=$lab/$(e2e_output e2e-offline)
if [ "$E2E_TARGET" = disk ]; then
	e2e_require sfdisk debugfs
fi
python=requests
if [ "$E2E_DISTRO" != ubuntu ]; then
	python=""
fi
e2e_recipe "$lab" e2e-offline "$E2E_RELEASE" "git" "$python"

e2e_run build "$lab" "$FROSTROOT" build --plain "${e2eTrustFlags[@]}"
e2e_expect_status 0 build
e2e_check "the lock records the instant the image is frozen at" e2e_matches "$lock" '^source_date_epoch = [1-9]'
if [ "$E2E_TARGET" = disk ]; then
	e2e_check "the lock records the disk's seed and sizes" e2e_matches "$lock" '^seed = "[0-9a-f-]{36}"'
	e2e_check "and the tools tree it was made with" e2e_contains "$lock" "[[disk_tools]]"
fi
entries=$(e2e_entries "$tarball")
e2e_check "the image has git" e2e_has_line "$entries" "./usr/bin/git"
if [ -n "$python" ]; then
	e2e_check "the lock records the Python wheels" e2e_contains "$lock" "[[pypi]]"
	e2e_check "the image's environment has requests" e2e_matches "$entries" '^\./opt/frostroot/venv/lib/python3[.0-9]*/site-packages/requests/__init__\.py$'
fi
lockBefore=$(e2e_sha "$lock")

e2e_run vendor "$lab" "$FROSTROOT" vendor --plain "${e2eTrustFlags[@]}"
e2e_expect_status 0 vendor

e2e_run offline1 "$lab" "$FROSTROOT" build --offline --plain
e2e_expect_status 0 offline1
e2e_check "the rebuild says it is reproducible" e2e_contains "$LAB/offline1.out" "byte for byte"
first=$(e2e_sha "$tarball")
mv "$tarball" "$LAB/offline1.$(basename "$tarball" | sed 's/.*\.\(tar\.gz\|img\)$/\1/')"

PYTHONPYCACHEPREFIX=$LAB/host-bytecode MKOSI_DNF=dnf e2e_run offline2 "$lab" "$FROSTROOT" build --offline --plain
e2e_expect_status 0 offline2
second=$(e2e_sha "$tarball")
e2e_check "the second rebuild holds no cache written under the host's PYTHONPYCACHEPREFIX" e2e_lacks "$(e2e_entries "$tarball")" "host-bytecode"
echo "  offline 1: $first"
echo "  offline 2: $second"
e2e_check "two offline rebuilds are byte-identical" test "$first" = "$second"
e2e_check "the rebuilds left the lock as it was" test "$lockBefore" = "$(e2e_sha "$lock")"
e2e_end
