#!/usr/bin/env bash
# proves: nothing the build trusts only for itself reaches the image: two online builds at one instant over HTTPS, one plain and one with --ca-bundle and --insecure, are the same bytes and name nothing of the build
# needs: the network (an HTTPS Ubuntu mirror), mmdebstrap, ubuntu-keyring, user namespaces, openssl
# takes: about 15 minutes: two online builds
#
# Two online builds of one recipe frozen at one SOURCE_DATE_EPOCH come out
# the same bytes, which makes them a leak detector: a setting meant only for
# the build that reaches the image carries the build's random work
# directory, and the two tarballs differ. That is how v0.8's --aptopt defect
# was found. Over an HTTPS mirror apt really reads the CA bundle and the
# verification settings the setup hook writes, so the flags are exercised,
# not just passed. If the archive publishes between the two builds their
# locks name other versions, and the scenario says INCONCLUSIVE rather than
# FAIL. FROSTROOT_E2E_HTTPS_MIRROR picks the mirror.
source "$(dirname "$0")/lib.sh"

e2e_begin no-build-leaks
e2e_require_bootstrap
e2e_require openssl
e2e_build_frostroot
mirror=${FROSTROOT_E2E_HTTPS_MIRROR:-https://archive.ubuntu.com/ubuntu}
e2e_certificate "frostroot e2e Corp Root CA" "$LAB/corp.key" "$LAB/corp-root.pem" || e2e_abort "openssl failed"
e2e_certificate "frostroot e2e Proxy Root CA" "$LAB/proxy.key" "$LAB/proxy-root.pem" || e2e_abort "openssl failed"
for variant in plain flags; do
	mkdir -p "$LAB/$variant/certs"
	cp "$LAB/corp-root.pem" "$LAB/$variant/certs/corp-root.pem"
	e2e_recipe "$LAB/$variant" e2e-leaks 24.04 "ca-certificates" "" '[certificates]
include = ["certs/corp-root.pem"]'
done
tarball=dist/e2e-leaks-ubuntu-24.04-amd64.tar.gz

# Any fixed instant will do; this one is 2025-09-19.
export SOURCE_DATE_EPOCH=1758240000
e2e_run plain "$LAB/plain" "$FROSTROOT" build --plain --mirror "$mirror"
e2e_expect_status 0 plain
e2e_run flags "$LAB/flags" "$FROSTROOT" build --plain --mirror "$mirror" --ca-bundle "$LAB/proxy-root.pem" --insecure
e2e_expect_status 0 flags
unset SOURCE_DATE_EPOCH

e2e_check "the flags' build said what --insecure gives up" e2e_contains "$LAB/flags.err" "warning: --insecure"
e2e_check "its lock says nothing of --ca-bundle" e2e_lacks "$LAB/flags/frostroot.lock" "proxy-root"
e2e_check "its lock marks nothing unverified, having no Python packages" e2e_lacks "$LAB/flags/frostroot.lock" "transport"
e2e_check_no_build_trust "$LAB/flags/$tarball"
e2e_unpack "$LAB/flags/$tarball" "$LAB/image"
e2e_check "the whole image unpacked, dpkg's status and all" test -s "$LAB/image/var/lib/dpkg/status"
e2e_check "no file anywhere in the image names the build's work root" e2e_tree_lacks "$LAB/image" "$XDG_CACHE_HOME/frostroot"

if ! e2e_same_file "$LAB/plain/frostroot.lock" "$LAB/flags/frostroot.lock"; then
	# Into a file first: under pipefail, diff's own exit status 1 would
	# make "diff | grep" false whatever grep found.
	diff "$LAB/plain/frostroot.lock" "$LAB/flags/frostroot.lock" > "$LAB/locks.diff"
	echo "  the two locks differ:"
	head -n 20 "$LAB/locks.diff" | sed 's/^/        /'
	if e2e_matches "$LAB/locks.diff" "^[<>] (version|sha256|size|filename) = "; then
		e2e_inconclusive "the archive published between the two builds; run the scenario again"
	fi
	e2e_abort "the two locks differ in something other than the archive's packages"
fi
e2e_check "the two builds wrote the same lock" true
e2e_check "the two images are byte-identical" e2e_same_file "$LAB/plain/$tarball" "$LAB/flags/$tarball"
if ! e2e_same_file "$LAB/plain/$tarball" "$LAB/flags/$tarball"; then
	e2e_unpack "$LAB/plain/$tarball" "$LAB/image-plain"
	echo "  the files that differ:"
	diff -rq --no-dereference "$LAB/image-plain" "$LAB/image" 2> /dev/null | head -20 | sed 's/^/        /'
fi
e2e_end
