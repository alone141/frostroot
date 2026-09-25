#!/usr/bin/env bash
# proves: frostroot capture, pointed at an image frostroot built, writes a recipe that validates and asks for what the original asked for
# needs: the network, mmdebstrap, ubuntu-keyring, user namespaces
# takes: about 10 minutes: an online build and a capture
#
# capture reads dpkg's status, apt's sources and the configuration of a
# real system, and the unit tests can only fake those files. An image
# frostroot built is a real system whose answers are known: the packages
# asked for, the release and the user.
source "$(dirname "$0")/lib.sh"

e2e_begin capture-roundtrip
e2e_require_bootstrap
e2e_build_frostroot
lab=$LAB/lab
tarball=$lab/dist/e2e-capture-ubuntu-24.04-amd64.tar.gz
e2e_recipe "$lab" e2e-capture 24.04 "git jq"

e2e_run build "$lab" "$FROSTROOT" build --plain
e2e_expect_status 0 build
e2e_unpack "$tarball" "$LAB/root"
e2e_check "the unpacked image has dpkg's status" test -s "$LAB/root/var/lib/dpkg/status"

# Every question answered with its default, which is what the image said.
mkdir -p "$LAB/captured"
yes '' | head -n 100 > "$LAB/answers.txt"
E2E_STDIN=$LAB/answers.txt e2e_run capture "$LAB/captured" "$FROSTROOT" capture --plain --root "$LAB/root"
e2e_expect_status 0 capture
recipe=$LAB/captured/frostroot.toml
e2e_check "capture wrote a recipe" test -s "$recipe"
e2e_check "and its report of what a recipe cannot carry" test -s "$LAB/captured/frostroot-capture.md"
e2e_check "the recipe asks for git" e2e_matches "$recipe" '^include = \[.*"git"'
e2e_check "and for jq" e2e_matches "$recipe" '^include = \[.*"jq"'
e2e_check "for the same release" e2e_has_line "$recipe" 'release = "24.04"'
e2e_check "and for the same user" e2e_has_line "$recipe" 'name = "student"'

e2e_run validate "$LAB/captured" "$FROSTROOT" validate
e2e_expect_status 0 validate
e2e_end
