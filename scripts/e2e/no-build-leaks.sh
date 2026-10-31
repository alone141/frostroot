#!/usr/bin/env bash
# proves: nothing the build trusts only for itself reaches the image: two online builds at one instant over HTTPS, one plain and one with --ca-bundle and --insecure, are the same bytes and name nothing of the build; for Ubuntu, Fedora with E2E_DISTRO=fedora, or a disk image with E2E_TARGET=disk, whose two builds differ only by the seed each drew
# needs: the network (an HTTPS Ubuntu mirror, or Fedora's), user namespaces, openssl, and mmdebstrap and ubuntu-keyring, or for Fedora mkosi with dnf, rpm, createrepo_c and bubblewrap; for a disk, bubblewrap and e2fsprogs and util-linux on the host
# takes: about 15 minutes for Ubuntu, 3 for Fedora, 25 for a disk: two online builds
#
# Two online builds of one recipe frozen at one SOURCE_DATE_EPOCH come out
# the same bytes, which makes them a leak detector: a setting meant only for
# the build that reaches the image carries the build's random work
# directory, and the two tarballs differ. That is how v0.8's --aptopt defect
# was found. Over an HTTPS mirror apt really reads the CA bundle and the
# verification settings the setup hook writes, so the flags are exercised,
# not just passed. If the archive publishes between the two builds their
# locks name other versions, and the scenario says INCONCLUSIVE rather than
# FAIL. FROSTROOT_E2E_HTTPS_MIRROR picks the mirror. A Fedora build reaches
# Fedora's mirrors over HTTPS through its metalinks, and takes neither a
# --mirror nor [certificates] yet: dnf reads the bundle and the switch.
source "$(dirname "$0")/lib.sh"

e2e_begin no-build-leaks
e2e_require_bootstrap
e2e_require openssl
if [ "$E2E_TARGET" = disk ]; then
	e2e_require sfdisk debugfs
fi
e2e_build_frostroot
mirror=${FROSTROOT_E2E_HTTPS_MIRROR:-https://archive.ubuntu.com/ubuntu}
e2e_certificate "frostroot e2e Corp Root CA" "$LAB/corp.key" "$LAB/corp-root.pem" || e2e_abort "openssl failed"
e2e_certificate "frostroot e2e Proxy Root CA" "$LAB/proxy.key" "$LAB/proxy-root.pem" || e2e_abort "openssl failed"
certificates='[certificates]
include = ["certs/corp-root.pem"]'
mirrorFlags=(--mirror "$mirror")
packageDatabase=var/lib/dpkg/status
if [ "$E2E_DISTRO" = fedora ]; then
	certificates=""
	mirrorFlags=()
	packageDatabase=usr/lib/sysimage/rpm/rpmdb.sqlite
fi
for variant in plain flags; do
	mkdir -p "$LAB/$variant/certs"
	cp "$LAB/corp-root.pem" "$LAB/$variant/certs/corp-root.pem"
	e2e_recipe "$LAB/$variant" e2e-leaks "$E2E_RELEASE" "ca-certificates" "" "$certificates"
done
tarball=$(e2e_output e2e-leaks)

# Any fixed instant will do; this one is 2025-09-19.
export SOURCE_DATE_EPOCH=1758240000
e2e_run plain "$LAB/plain" "$FROSTROOT" build --plain "${mirrorFlags[@]}"
e2e_expect_status 0 plain
e2e_run flags "$LAB/flags" "$FROSTROOT" build --plain "${mirrorFlags[@]}" --ca-bundle "$LAB/proxy-root.pem" --insecure
e2e_expect_status 0 flags
unset SOURCE_DATE_EPOCH

e2e_check "the flags' build said what --insecure gives up" e2e_contains "$LAB/flags.err" "warning: --insecure"
e2e_check "its lock says nothing of --ca-bundle" e2e_lacks "$LAB/flags/frostroot.lock" "proxy-root"
e2e_check "its lock marks nothing unverified, having no Python packages" e2e_lacks "$LAB/flags/frostroot.lock" "transport"
if [ "$E2E_DISTRO" = fedora ]; then
	e2e_check_no_fedora_build_trust "$LAB/flags/$tarball"
else
	e2e_check_no_build_trust "$LAB/flags/$tarball"
fi
if [ "$E2E_TARGET" = disk ]; then
	image=$(e2e_disk_dump "$LAB/flags/$tarball") || e2e_abort "cannot read the flags' disk image"
	plainImage=$(e2e_disk_dump "$LAB/plain/$tarball") || e2e_abort "cannot read the plain disk image"
else
	image=$LAB/image
	plainImage=$LAB/image-plain
	e2e_unpack "$LAB/flags/$tarball" "$image"
fi
e2e_check "the whole image unpacked, the package database and all" test -s "$image/$packageDatabase"
e2e_check "no file anywhere in the image names the build's work root" e2e_tree_lacks "$image" "$XDG_CACHE_HOME/frostroot"

# Every online disk build draws a seed of its own, so two disks differ in
# the seed and in what carries it: the locks' seed lines, and in the
# image fstab and grub.cfg, which name the root's UUID. Everything else
# must be the same.
locksDiffer=0
if ! e2e_same_file "$LAB/plain/frostroot.lock" "$LAB/flags/frostroot.lock"; then
	# Into a file first: under pipefail, diff's own exit status 1 would
	# make "diff | grep" false whatever grep found.
	diff "$LAB/plain/frostroot.lock" "$LAB/flags/frostroot.lock" > "$LAB/locks.diff"
	# A disk's two locks may differ in their seed lines and nothing else:
	# diff's hunk headers, and the two seed lines, are all that may be there.
	if [ "$E2E_TARGET" = disk ] && ! grep -vqE '^(---|[0-9,]+c[0-9,]+|[<>] seed = )' "$LAB/locks.diff"; then
		: # the seeds alone
	else
		locksDiffer=1
		echo "  the two locks differ:"
		head -n 20 "$LAB/locks.diff" | sed 's/^/        /'
		if e2e_matches "$LAB/locks.diff" "^[<>] (version|sha256|size|filename) = "; then
			e2e_inconclusive "the archive published between the two builds; run the scenario again"
		fi
		e2e_abort "the two locks differ in something other than the archive's packages"
	fi
fi
e2e_check "the two builds wrote the same lock, but for a disk's seed" test "$locksDiffer" -eq 0
if [ "$E2E_TARGET" = disk ]; then
	diff -rq --no-dereference "$plainImage" "$image" 2> /dev/null | grep -vE '/(etc/fstab|boot/grub/grub.cfg)$' > "$LAB/images.diff"
	e2e_check "the two disks' roots differ only in what carries the seed" test ! -s "$LAB/images.diff"
	if [ -s "$LAB/images.diff" ]; then
		echo "  the files that differ:"
		head -n 20 "$LAB/images.diff" | sed 's/^/        /'
	fi
else
	e2e_check "the two images are byte-identical" e2e_same_file "$LAB/plain/$tarball" "$LAB/flags/$tarball"
	if ! e2e_same_file "$LAB/plain/$tarball" "$LAB/flags/$tarball"; then
		e2e_unpack "$LAB/plain/$tarball" "$plainImage"
		echo "  the files that differ:"
		diff -rq --no-dereference "$plainImage" "$image" 2> /dev/null | head -20 | sed 's/^/        /'
	fi
fi
e2e_end
