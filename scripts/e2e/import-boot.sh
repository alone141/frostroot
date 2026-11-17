#!/usr/bin/env bash
# proves: frostroot import registers a tarball built elsewhere on a Windows without wsl --import, and the distribution starts: the user, passwordless sudo, DNS, the locale, the timezone, the capability on ping kept; a name already taken and a file that is not a tarball are refused and leave nothing; nothing of the import stays beside the install directory
# needs: Windows: Git Bash there, or a WSL distribution on a Windows that starts one distribution from inside another; E2E_TARBALL and E2E_FROSTROOT_WINDOWS
# takes: about 2 minutes: three imports, of which one registers
#
# wsl-boot builds and imports on one machine. A machine with only WSL 1
# cannot build, so this takes what was built elsewhere: E2E_TARBALL, the
# tarball of a recipe as e2e_recipe writes it (the user student, UTC,
# en_US.UTF-8, git among its packages), such as the one another scenario's
# lab holds in dist/, and E2E_FROSTROOT_WINDOWS, the Windows binary.
#
# Where to run it. Windows 10 before version 1903 starts no distribution
# from inside another ("Operation not supported"), so there neither import
# nor this scenario can run in a WSL shell: run it in Git Bash, with
# FROSTROOT_E2E_ROOT naming a directory for the lab, since Git Bash has no
# /var/tmp:
#
#   FROSTROOT_E2E_ROOT=/tmp/frostroot-e2e E2E_TARBALL=... E2E_FROSTROOT_WINDOWS=... scripts/e2e/run.sh import-boot
#
# From a WSL distribution on a newer Windows it runs as wsl-boot does, with
# both files on a Windows drive, under /mnt: Windows reads no WSL 1
# distribution's own files.
#
# The distribution is frostroot-e2e-import unless E2E_IMPORT_NAME names
# another, and is unregistered again at the end, whatever happened. systemd
# is not checked: WSL 1 starts none, and the verdict says so. Nothing here
# uses wsl.exe --list or --unregister, which the wsl.exe of Windows 10 1809
# does not have; wslconfig.exe does both on every Windows with WSL.
# shellcheck source=scripts/e2e/lib.sh
source "$(dirname "$0")/lib.sh"

# wsl.exe -e on Windows 10 1809 starts a process with umask 000, and what
# this writes under the e2e root would be writable by everyone.
umask 022

# In Git Bash the paths are converted with cygpath, and arguments that look
# like POSIX paths must reach wsl.exe as they are: Git Bash would turn
# /usr/bin/ping into a path under its own installation.
case "$(uname -s)" in
MINGW* | MSYS*)
	gitBash=1
	export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'
	;;
*) gitBash=0 ;;
esac
to_windows() { # PATH: as Windows names it
	if [ "$gitBash" = 1 ]; then cygpath -w "$1"; else wslpath -w "$1"; fi
}
from_windows() { # PATH: as this shell names it
	if [ "$gitBash" = 1 ]; then cygpath -u "$1"; else wslpath -u "$1"; fi
}

e2e_begin import-boot
if [ -z "${E2E_TARBALL:-}" ] || [ -z "${E2E_FROSTROOT_WINDOWS:-}" ]; then
	echo "SKIP import-boot: needs E2E_TARBALL, a tarball built elsewhere, and E2E_FROSTROOT_WINDOWS, the Windows binary"
	exit 4
fi
if ! command -v wsl.exe > /dev/null 2>&1 || ! command -v wslconfig.exe > /dev/null 2>&1 || ! command -v cmd.exe > /dev/null 2>&1; then
	echo "SKIP import-boot: wsl.exe, wslconfig.exe and cmd.exe are not reachable, so this is neither Windows nor a WSL distribution with interop"
	exit 4
fi
if [ "$gitBash" = 1 ]; then
	e2e_require cygpath
else
	e2e_require wslpath
fi
for file in "$E2E_TARBALL" "$E2E_FROSTROOT_WINDOWS"; do
	[ -f "$file" ] || e2e_abort "$file is not a file"
	if [ "$gitBash" = 0 ]; then
		case "$(readlink -f "$file")" in
		/mnt/?/*) ;;
		*) e2e_abort "$file is not on a Windows drive, under /mnt, where Windows can read it" ;;
		esac
	fi
done
distro=${E2E_IMPORT_NAME:-frostroot-e2e-import}
frostrootWindows=$(readlink -f "$E2E_FROSTROOT_WINDOWS")
tarballForWindows=$(to_windows "$(readlink -f "$E2E_TARBALL")")

# windows_text strips what Windows' own programs print: UTF-16 with NULs,
# and CRLF.
windows_text() { tr -d '\0\r'; }

# in_image COMMAND... runs a command in the imported distribution as its
# default user, through a login shell, which is what a person gets, and
# prints what it printed.
in_image() {
	wsl.exe -d "$distro" -e bash -lc "$*" 2>&1 | tr -d '\r'
}

# registered reports whether the distribution exists. wslconfig marks the
# default one after its name, so only the first word of a line is compared.
registered() {
	local list
	list=$(wslconfig.exe /l 2> /dev/null | windows_text | awk '{print $1}')
	grep -qx -- "$distro" <<< "$list"
}

# Predicates over what the image said, for e2e_check, which calls them by
# name: shellcheck sees no call and takes them for unreachable.
# shellcheck disable=SC2317
says() { grep -qE -- "$1" <<< "$2"; }         # REGEX TEXT
# shellcheck disable=SC2317
never_says() { ! grep -qiE -- "$1" <<< "$2"; } # REGEX TEXT
# shellcheck disable=SC2317
holds_only() { # DIR NAME...: the directory holds these names and no other
	local dir=$1 held wanted
	shift
	held=$(find "$dir" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort | tr '\n' ' ')
	wanted=$(printf '%s\n' "$@" | sort | tr '\n' ' ')
	[ "$held" = "$wanted" ]
}

# unregister removes the distribution and what this scenario made on the
# Windows side. Windows removes the root file system; what is left of the
# install directory is its empty temp directory.
unregister() {
	if registered; then
		wslconfig.exe /u "$distro" 2>&1 | windows_text | sed 's/^/  /'
	fi
	if [ -n "${parentDir:-}" ] && [ -d "$parentDir" ]; then
		rmdir "$installDir/temp" "$installDir" 2> /dev/null || true
		rm -f "$parentDir/not-a-tarball.tar.gz"
		rmdir "$parentDir" 2> /dev/null || true
	fi
}
trap unregister EXIT

# Where the distribution's files go: under the Windows user's local data.
localAppData=$(cmd.exe /c 'echo %LOCALAPPDATA%' 2> /dev/null | windows_text)
[ -n "$localAppData" ] || e2e_abort "cmd.exe did not say where %LOCALAPPDATA% is"
parentDir=$(from_windows "$localAppData")/frostroot-e2e-import
installDir=$parentDir/$distro
if registered; then
	trap - EXIT
	e2e_abort "a distribution named $distro is registered already; name another with E2E_IMPORT_NAME"
fi
if [ -e "$parentDir" ]; then
	trap - EXIT
	e2e_abort "$parentDir exists already; remove it first"
fi
mkdir -p "$parentDir" || e2e_abort "cannot create $parentDir"
installDirForWindows=$(to_windows "$parentDir")\\$distro

e2e_run import "$parentDir" "$frostrootWindows" import "$distro" "$installDirForWindows" "$tarballForWindows"
e2e_expect_status 0 import
e2e_check "import registered the distribution" registered
registered || e2e_abort "nothing to start"
e2e_check "import said what it registered" e2e_contains "$LAB/import.out" "Registered $distro"
e2e_check "it left the two ACL records out of its copy" e2e_matches "$LAB/import.err" 'left out 2 such records'
e2e_check "its copy of the tarball is gone from beside the install directory" holds_only "$parentDir" "$distro"
e2e_check "and its copy of itself from inside it" holds_only "$installDir" rootfs temp

# wsl-boot's checks, but for systemd.
whoami=$(in_image whoami)
e2e_check "whoami is the recipe's user ($whoami)" test "$whoami" = student
sudo=$(in_image sudo -n id -u)
e2e_check "sudo -n needs no password (uid $sudo)" test "$sudo" = 0
hosts=$(in_image getent hosts archive.ubuntu.com)
e2e_check "DNS resolves archive.ubuntu.com" says 'archive\.ubuntu\.com' "$hosts"
locale=$(in_image 'locale 2>&1')
e2e_check "locale gives no warning" never_says 'warning|cannot set' "$locale"
e2e_check "and is the recipe's" says '^LANG=en_US\.UTF-8$' "$locale"
zone=$(in_image date +%Z)
e2e_check "the timezone is the recipe's ($zone)" test "$zone" = UTC
capability=$(in_image "PATH=\$PATH:/usr/sbin:/sbin getcap /usr/bin/ping")
e2e_check "ping kept its capability through Windows' unpacking ($capability)" says 'cap_net_raw[=+]ep' "$capability"
git=$(in_image git --version)
e2e_check "a package the recipe asked for runs ($git)" says '^git version [0-9]' "$git"

# What import refuses, and that a refusal makes nothing.
e2e_run import-again "$parentDir" "$frostrootWindows" import "$distro" "$installDirForWindows-again" "$tarballForWindows"
e2e_check "a name already registered is refused with exit 1 (exit $e2eStatus)" test "$e2eStatus" -eq 1
e2e_check "and says which name" e2e_contains "$LAB/import-again.err" "$distro is already registered"
echo "this is not a tarball" > "$parentDir/not-a-tarball.tar.gz"
e2e_run import-junk "$parentDir" "$frostrootWindows" import "$distro-junk" "$installDirForWindows-junk" "$(to_windows "$parentDir/not-a-tarball.tar.gz")"
e2e_check "a file that is not a tarball is refused with exit 1 (exit $e2eStatus)" test "$e2eStatus" -eq 1
rm -f "$parentDir/not-a-tarball.tar.gz"
e2e_check "and neither refusal left a directory or a file" holds_only "$parentDir" "$distro"

unregister
e2e_check "wslconfig /u unregistered it" e2e_not registered
e2e_check "and nothing this scenario made is left" test ! -e "$parentDir"
echo "  note  systemd was not checked: WSL 1 starts none"
e2e_end
