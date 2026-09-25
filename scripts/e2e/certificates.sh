#!/usr/bin/env bash
# proves: [certificates] reaches the image and the lock and --ca-bundle reaches neither; offline rebuilds with and without --ca-bundle are the same bytes; a certificate changed under the lock is refused
# needs: the network, mmdebstrap, ubuntu-keyring, user namespaces, openssl
# takes: about 30 minutes: an online build, vendor, two offline builds
#
# Two private authorities. The recipe ships one in [certificates]; the build
# is told to trust the other with --ca-bundle, the way a proxy's authority is
# trusted for fetching without being handed to everyone who imports the
# image. The recipe asks for a Python package because the Python step is
# where --ca-bundle's file is uploaded into the image, and where it has to be
# deleted again.
source "$(dirname "$0")/lib.sh"

e2e_begin certificates
e2e_require_bootstrap
e2e_require openssl
e2e_build_frostroot
lab=$LAB/lab
lock=$lab/frostroot.lock
tarball=$lab/dist/e2e-certs-ubuntu-24.04-amd64.tar.gz
mkdir -p "$lab/certs"
e2e_certificate "frostroot e2e Corp Root CA" "$LAB/corp.key" "$lab/certs/corp-root.pem" ||
	e2e_abort "openssl could not make the recipe's authority"
e2e_certificate "frostroot e2e Proxy Root CA" "$LAB/proxy.key" "$LAB/proxy-root.pem" ||
	e2e_abort "openssl could not make the build's authority"
e2e_recipe "$lab" e2e-certs 24.04 "ca-certificates openssl" "requests" '[certificates]
include = ["certs/corp-root.pem"]'

e2e_run build "$lab" "$FROSTROOT" build --plain --ca-bundle "$LAB/proxy-root.pem"
e2e_expect_status 0 build
e2e_check "the lock names the recipe's certificate" e2e_contains "$lock" "path = 'certs/corp-root.pem'"
e2e_check "the lock records that file's digest" e2e_contains "$lock" "sha256 = '$(e2e_sha "$lab/certs/corp-root.pem")'"
e2e_check "the lock says nothing of --ca-bundle" e2e_lacks "$lock" "proxy-root"

entries=$(e2e_entries "$tarball")
e2e_check "the image holds the recipe's authority" e2e_has_line "$entries" "./usr/local/share/ca-certificates/corp-root.crt"
e2e_check_no_build_trust "$tarball"
e2e_unpack "$tarball" "$LAB/image" ./etc/ssl/certs/ca-certificates.crt
bundle=$LAB/image/etc/ssl/certs/ca-certificates.crt
e2e_check "the image has a certificate store to ask" test -s "$bundle"
e2e_check "the image's store trusts the recipe's authority" openssl verify -CAfile "$bundle" "$lab/certs/corp-root.pem"
e2e_check "the image's store does not trust --ca-bundle's" e2e_not openssl verify -CAfile "$bundle" "$LAB/proxy-root.pem"

e2e_run vendor "$lab" "$FROSTROOT" vendor --plain --ca-bundle "$LAB/proxy-root.pem"
e2e_expect_status 0 vendor
lockBefore=$(e2e_sha "$lock")

e2e_run offline1 "$lab" "$FROSTROOT" build --offline --plain
e2e_expect_status 0 offline1
first=$(e2e_sha "$tarball")
mv "$tarball" "$LAB/offline1.tar.gz"
e2e_run offline2 "$lab" "$FROSTROOT" build --offline --plain --ca-bundle "$LAB/proxy-root.pem"
e2e_expect_status 0 offline2
second=$(e2e_sha "$tarball")
echo "  without --ca-bundle: $first"
echo "  with --ca-bundle:    $second"
e2e_check "--ca-bundle changes no byte of an offline rebuild" test "$first" = "$second"
e2e_check "the rebuilds left the lock as it was" test "$lockBefore" = "$(e2e_sha "$lock")"

# An offline rebuild installs the certificate file beside the recipe, so a
# file that changed since the lock was written must stop it before it
# starts.
cp "$lab/certs/corp-root.pem" "$LAB/corp-root.pem.orig"
e2e_certificate "Someone Else" "$LAB/else.key" "$lab/certs/corp-root.pem" || e2e_abort "openssl failed"
e2e_run changed "$lab" "$FROSTROOT" build --offline --plain
e2e_check "an offline rebuild refuses a certificate changed under the lock" test "$e2eStatus" -eq 1
e2e_check "and names it" e2e_contains "$LAB/changed.err" "certificate certs/corp-root.pem changed since the lock was written"
cp "$LAB/corp-root.pem.orig" "$lab/certs/corp-root.pem"
e2e_end
