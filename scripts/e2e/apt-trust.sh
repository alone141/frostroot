#!/usr/bin/env bash
# proves: the apt that mmdebstrap runs obeys the trust settings frostroot's setup hook writes (a CA bundle, verification switched off), and the cleanup hook leaves none of them in the image
# needs: the network (an HTTPS Ubuntu mirror), mmdebstrap, ubuntu-keyring, user namespaces, openssl
# takes: about 10 minutes: three bootstraps of noble's essential packages
#
# mmdebstrap runs directly, without frostroot, because only that way can apt
# be handed a CA bundle that cannot verify the mirror: frostroot always puts
# the host's roots in the bundle. Three arms, over HTTPS:
#   refuse    CaInfo names a bogus authority alone. Every fetch must fail,
#             which proves apt reads the file the hook writes.
#   trust     CaInfo names the host's roots and the bogus authority. It
#             builds, the way [certificates] and --ca-bundle build.
#   insecure  The bogus bundle again, with Verify-Peer and Verify-Host off
#             as --insecure writes them. It builds, so they are obeyed.
# The hooks are frostroot's own text, which internal/builder's
# bootstrap_test.go and insecure_test.go pin; change them there and here
# together. FROSTROOT_E2E_HTTPS_MIRROR picks the mirror.
source "$(dirname "$0")/lib.sh"

e2e_begin apt-trust
e2e_require_bootstrap
e2e_require openssl
mirror=${FROSTROOT_E2E_HTTPS_MIRROR:-https://archive.ubuntu.com/ubuntu}
conf=/etc/apt/apt.conf.d/99frostroot-build-ca
e2e_certificate "frostroot e2e bogus authority" "$LAB/bogus.key" "$LAB/bogus.pem" || e2e_abort "openssl failed"
cat /etc/ssl/certs/ca-certificates.crt "$LAB/bogus.pem" > "$LAB/host-and-bogus.pem"
chmod 644 "$LAB"/*.pem

# setup_hook SETTING... prints aptTrustSetupHook's text for these settings.
setup_hook() {
	local quoted="" setting
	for setting in "$@"; do
		quoted+=" '$setting'"
	done
	echo "mkdir -p \"\$1/etc/apt/apt.conf.d\" && printf '%s\\n'$quoted > \"\$1$conf\""
}

# arm NAME SETTING... bootstraps noble with those settings in place.
arm() {
	local name=$1
	shift
	mkdir -p "$LAB/tmp-$name"
	chmod 1777 "$LAB/tmp-$name"
	e2e_run "$name" "$LAB" env TMPDIR="$LAB/tmp-$name" mmdebstrap --mode=unshare --variant=essential \
		--architectures=amd64 --keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg \
		--setup-hook="$(setup_hook "$@")" --customize-hook="rm -f \"\$1$conf\"" \
		noble "$LAB/$name.tar.gz" "deb $mirror noble main"
}

arm refuse "Acquire::https::CaInfo \"$LAB/bogus.pem\";"
e2e_check "apt refuses the mirror when the hook's bundle cannot verify it" test "$e2eStatus" -ne 0
e2e_check "and says the certificate is why" grep -qE "Certificate verification failed|certificate verify failed" "$LAB/refuse.out" "$LAB/refuse.err"

arm trust "Acquire::https::CaInfo \"$LAB/host-and-bogus.pem\";"
e2e_expect_status 0 trust
e2e_check_no_build_trust "$LAB/trust.tar.gz"

arm insecure "Acquire::https::CaInfo \"$LAB/bogus.pem\";" 'Acquire::https::Verify-Peer "false";' 'Acquire::https::Verify-Host "false";'
e2e_expect_status 0 insecure
e2e_check_no_build_trust "$LAB/insecure.tar.gz"
e2e_end
