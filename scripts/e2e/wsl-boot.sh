#!/usr/bin/env bash
# proves: an image frostroot built imports into WSL and boots: the user, passwordless sudo, systemd, DNS, the locale, the timezone, the Python environment on PATH, the recipe's certificate authority trusted, and apt still verifying TLS
# needs: the network, mmdebstrap, ubuntu-keyring, user namespaces, openssl, and WSL itself: wsl.exe reachable from this distribution
# takes: about 15 minutes: an online build, an import, a boot
#
# The README's release check, which no CI runner can make, because none
# can run wsl --import. From inside WSL, wsl.exe is reachable through
# interop, and Windows reads the tarball through \\wsl.localhost, which is
# the path frostroot's own import hint prints. The image is imported as a
# throwaway distribution, frostroot-e2e-boot, that is removed again at the
# end, whatever happened. E2E_FROSTROOT=/path/to/frostroot-linux-amd64
# puts a release's binary through it instead of the checkout's.
source "$(dirname "$0")/lib.sh"

e2e_begin wsl-boot
e2e_require_bootstrap
e2e_require openssl wslpath
if ! command -v wsl.exe > /dev/null 2>&1 || ! command -v cmd.exe > /dev/null 2>&1; then
	echo "SKIP wsl-boot: wsl.exe is not reachable, so this is not a WSL distribution with interop"
	exit 4
fi
e2e_build_frostroot
distro=frostroot-e2e-boot
lab=$LAB/lab
tarball=$lab/dist/e2e-boot-ubuntu-24.04-amd64.tar.gz
mkdir -p "$lab/certs"
e2e_certificate "frostroot e2e Corp Root CA" "$LAB/corp.key" "$lab/certs/corp-root.pem" || e2e_abort "openssl failed"
e2e_recipe "$lab" e2e-boot 24.04 "git openssl" "requests" '[certificates]
include = ["certs/corp-root.pem"]'

# windows_text strips what wsl.exe prints: UTF-16 with NULs, and CRLF.
windows_text() { tr -d '\0\r'; }

# in_image COMMAND... runs a command in the imported distribution as its
# default user, through a login shell, which is what a person gets, and
# prints what it printed.
in_image() {
	wsl.exe -d "$distro" -e bash -lc "$*" 2>&1 | tr -d '\r'
}

# registered reports whether the distribution exists.
registered() {
	local list
	list=$(wsl.exe --list --quiet 2> /dev/null | windows_text)
	grep -qx -- "$distro" <<< "$list"
}

# Predicates over what the image said, for e2e_check.
is_running_or_degraded() { [ "$1" = running ] || [ "$1" = degraded ]; }
says() { grep -qE -- "$1" <<< "$2"; }         # REGEX TEXT
never_says() { ! grep -qiE -- "$1" <<< "$2"; } # REGEX TEXT

unregister() {
	if registered; then
		wsl.exe --unregister "$distro" 2>&1 | windows_text | sed 's/^/  /'
	fi
	if [ -n "${installDir:-}" ] && [ -d "$installDir" ]; then
		rm -rf "$installDir"
		# The parent this scenario made, when nothing else is in it.
		rmdir "$(dirname "$installDir")" 2> /dev/null || true
	fi
}
trap unregister EXIT

e2e_run build "$lab" "$FROSTROOT" build --plain
e2e_expect_status 0 build
e2e_check "the build printed the wsl --import hint for this tarball" e2e_contains "$LAB/build.out" "wsl --import e2e-boot"

# Where the distribution's disk goes: under the Windows user's local data,
# the way wsl --import is normally used, reached from here through /mnt.
localAppData=$(cmd.exe /c 'echo %LOCALAPPDATA%' 2> /dev/null | windows_text)
[ -n "$localAppData" ] || e2e_abort "cmd.exe did not say where %LOCALAPPDATA% is"
installDir=$(wslpath -u "$localAppData")/frostroot-e2e/boot
unregister
mkdir -p "$installDir" || e2e_abort "cannot create $installDir"
tarballForWindows=$(wslpath -w "$tarball")
echo "--- importing $tarballForWindows as $distro into $(wslpath -w "$installDir")"
started=$(date +%s)
wsl.exe --import "$distro" "$(wslpath -w "$installDir")" "$tarballForWindows" --version 2 2>&1 | windows_text | sed 's/^/  /'
echo "--- imported in $(($(date +%s) - started)) s"
e2e_check "wsl --import registered the distribution" registered
registered || e2e_abort "nothing to boot"

# The README's checks, in its order, then the image's own promises.
whoami=$(in_image whoami)
e2e_check "whoami is the recipe's user ($whoami)" test "$whoami" = student
sudo=$(in_image sudo -n id -u)
e2e_check "sudo -n needs no password (uid $sudo)" test "$sudo" = 0
systemd=$(timeout 180 wsl.exe -d "$distro" -e bash -lc 'systemctl is-system-running --wait' 2>&1 | tr -d '\r' | tail -n 1)
e2e_check "systemd is running or degraded ($systemd)" is_running_or_degraded "$systemd"
hosts=$(in_image getent hosts archive.ubuntu.com)
e2e_check "DNS resolves archive.ubuntu.com" says 'archive\.ubuntu\.com' "$hosts"
locale=$(in_image 'locale 2>&1')
e2e_check "locale gives no warning" never_says 'warning|cannot set' "$locale"
e2e_check "and is the recipe's" says '^LANG=en_US\.UTF-8$' "$locale"
zone=$(in_image date +%Z)
e2e_check "the timezone is the recipe's ($zone)" test "$zone" = UTC
python=$(in_image command -v python3)
e2e_check "python3 on PATH is the environment's ($python)" test "$python" = /opt/frostroot/venv/bin/python3
requests=$(in_image "python3 -c 'import requests; print(requests.__version__)'")
e2e_check "import requests works ($requests)" says '^[0-9]+\.[0-9]+' "$requests"
pip=$(in_image pip --version)
e2e_check "pip is the pinned resolver ($pip)" says 'pip 24\.3\.1' "$pip"
verify=$(in_image openssl verify -CAfile /etc/ssl/certs/ca-certificates.crt /usr/local/share/ca-certificates/corp-root.crt)
e2e_check "the image trusts the recipe's authority" says ': OK$' "$verify"
aptConf=$(in_image 'cat /etc/apt/apt.conf.d/* 2>/dev/null')
e2e_check "apt in the booted image verifies TLS" never_says 'Verify-Peer|CaInfo' "$aptConf"
e2e_check "and holds no file of the build's" never_says 'frostroot-build-ca' "$aptConf"
e2e_end
