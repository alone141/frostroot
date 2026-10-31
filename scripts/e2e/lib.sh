# Shared by the scenarios in scripts/e2e. Source it; do not run it.
#
# Every scenario works under one root, so that one command removes all of it
# (scripts/e2e/clean.sh):
#
#   $FROSTROOT_E2E_ROOT, by default /var/tmp/frostroot-e2e-<uid>
#     cache/        XDG_CACHE_HOME for every frostroot run: its work root,
#                   cache/frostroot/build-*, its package-index cache, and
#                   Fedora's package cache
#     <scenario>/   one scenario's lab: recipes, locks, dist/, logs, images
#     <scenario>.log  what scripts/e2e/run.sh printed while it ran
#
# The root is under /var/tmp for the reason frostroot's own default work
# root is: mmdebstrap runs in a user namespace that cannot enter a 0750 home
# directory, and a scenario that runs mmdebstrap itself needs its files
# where the namespace can reach them. /tmp would not do: WSL empties it.
#
# A scenario calls e2e_begin, then e2e_run for each command and e2e_check
# for each thing it asserts, and ends with e2e_end, which prints PASS or FAIL
# and exits 0 or 1. Exit 3 is INCONCLUSIVE (the world moved under the
# scenario, such as the archive publishing between two builds) and exit 4
# SKIP (a tool it needs is missing).

set -uo pipefail

e2eRepo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
E2E_ROOT=${FROSTROOT_E2E_ROOT:-/var/tmp/frostroot-e2e-$(id -u)}
# The family and release the scenarios build: Ubuntu 24.04, the release the
# runs the README records were made with, unless E2E_RELEASE names another;
# with E2E_DISTRO=fedora, Fedora 44.
E2E_DISTRO=${E2E_DISTRO:-ubuntu}
case "$E2E_DISTRO" in
ubuntu) E2E_RELEASE=${E2E_RELEASE:-24.04} ;;
fedora) E2E_RELEASE=${E2E_RELEASE:-44} ;;
*)
	echo "E2E_DISTRO=$E2E_DISTRO is not a family frostroot builds: ubuntu or fedora" >&2
	exit 2
	;;
esac
# The target the scenarios build: a tarball for WSL, or with
# E2E_TARGET=disk a raw disk image, which only an Ubuntu recipe makes so far.
E2E_TARGET=${E2E_TARGET:-wsl}
case "$E2E_TARGET" in
wsl | disk) ;;
*)
	echo "E2E_TARGET=$E2E_TARGET is not a target frostroot builds: wsl or disk" >&2
	exit 2
	;;
esac
if [ "$E2E_TARGET" = disk ] && [ "$E2E_DISTRO" != ubuntu ]; then
	echo "E2E_TARGET=disk builds Ubuntu alone so far, and E2E_DISTRO is $E2E_DISTRO" >&2
	exit 2
fi
e2eChecks=0
e2eFailures=0
e2eStatus=0
# E2E_CA_BUNDLE names a PEM file the builds that fetch over TLS trust, for
# a host behind a network that inspects it: the scenarios that fetch
# Python packages pass it as --ca-bundle, which reaches neither the image
# nor the lock, so their verdicts mean what they mean without it.
e2eTrustFlags=()
if [ -n "${E2E_CA_BUNDLE:-}" ]; then
	e2eTrustFlags=(--ca-bundle "$E2E_CA_BUNDLE")
fi

# e2e_begin NAME starts a scenario in a fresh, empty lab: $LAB.
e2e_begin() {
	e2eName=$1
	case "$E2E_ROOT" in
	/mnt/*)
		echo "FROSTROOT_E2E_ROOT=$E2E_ROOT is a Windows drive; frostroot refuses to build there" >&2
		exit 2
		;;
	/?*) ;;
	*)
		echo "FROSTROOT_E2E_ROOT=$E2E_ROOT must be an absolute path" >&2
		exit 2
		;;
	esac
	mkdir -p "$E2E_ROOT/cache" || exit 2
	chmod 755 "$E2E_ROOT" "$E2E_ROOT/cache"
	LAB=$E2E_ROOT/$e2eName
	e2e_remove "$LAB" || exit 2
	mkdir -p "$LAB" && chmod 755 "$LAB"
	export XDG_CACHE_HOME=$E2E_ROOT/cache
	unset SOURCE_DATE_EPOCH
	export LC_ALL=C.UTF-8
	e2eStarted=$(date +%s)
	echo "=== e2e $e2eName in $LAB"
}

# e2e_require TOOL... ends the scenario as SKIP when a tool is missing.
e2e_require() {
	local tool missing=()
	for tool in "$@"; do
		command -v "$tool" > /dev/null 2>&1 || missing+=("$tool")
	done
	if [ "${#missing[@]}" -gt 0 ]; then
		echo "SKIP $e2eName: needs ${missing[*]}"
		exit 4
	fi
}

# e2e_require_bootstrap ends the scenario as SKIP unless a real build of
# $E2E_DISTRO can run here: for Ubuntu, mmdebstrap and the archive keyring;
# for Fedora, mkosi, what it runs on the host, and the uid maps unless this
# is root. frostroot's own preflight checks mkosi's version.
e2e_require_bootstrap() {
	if [ "$E2E_DISTRO" = fedora ]; then
		e2e_require mkosi dnf rpm createrepo_c bwrap gzip
		if [ "$(id -u)" -ne 0 ]; then
			e2e_require newuidmap newgidmap
		fi
		return 0
	fi
	e2e_require mmdebstrap
	if [ ! -e /usr/share/keyrings/ubuntu-archive-keyring.gpg ]; then
		echo "SKIP $e2eName: needs ubuntu-keyring (sudo apt install ubuntu-keyring)"
		exit 4
	fi
	if [ "$E2E_TARGET" = disk ]; then
		# The disk tools tree runs through bubblewrap, and goes with unshare.
		e2e_require bwrap
		if [ "$(id -u)" -ne 0 ]; then
			e2e_require unshare newuidmap newgidmap
		fi
	fi
}

# e2e_require_wsl_target ends the scenario as SKIP when E2E_TARGET names a
# disk: what it checks is a tarball's.
e2e_require_wsl_target() {
	if [ "$E2E_TARGET" != wsl ]; then
		echo "SKIP $e2eName: a tarball's alone, and E2E_TARGET is $E2E_TARGET"
		exit 4
	fi
}

# e2e_require_ubuntu ends the scenario as SKIP when E2E_DISTRO names
# another family: what it checks, such as [certificates], Python packages or
# capture, frostroot does for Ubuntu alone so far.
e2e_require_ubuntu() {
	if [ "$E2E_DISTRO" != ubuntu ]; then
		echo "SKIP $e2eName: Ubuntu's alone so far, and E2E_DISTRO is $E2E_DISTRO"
		exit 4
	fi
}

# e2e_tarball NAME prints where a build of the recipe named NAME places its
# tarball, relative to the recipe's directory.
e2e_tarball() {
	printf 'dist/%s-%s-%s-amd64.tar.gz' "$1" "$E2E_DISTRO" "$E2E_RELEASE"
}

# e2e_output NAME prints where a build of the recipe named NAME places what
# it makes for $E2E_TARGET: the tarball, or the raw disk image.
e2e_output() {
	if [ "$E2E_TARGET" = disk ]; then
		printf 'dist/%s-%s-%s-amd64.img' "$1" "$E2E_DISTRO" "$E2E_RELEASE"
	else
		e2e_tarball "$1"
	fi
}

# e2e_build_frostroot builds the checkout's binary into the lab: $FROSTROOT.
# With E2E_FROSTROOT naming a binary, that one is copied in instead, which
# is how a release's own binary is put through a scenario.
e2e_build_frostroot() {
	FROSTROOT=$LAB/frostroot
	if [ -n "${E2E_FROSTROOT:-}" ]; then
		if [ ! -f "$E2E_FROSTROOT" ]; then
			e2e_abort "E2E_FROSTROOT=$E2E_FROSTROOT is not a file"
		fi
		cp "$E2E_FROSTROOT" "$FROSTROOT" && chmod 755 "$FROSTROOT" || e2e_abort "could not copy $E2E_FROSTROOT"
	else
		e2e_require go
		if ! (cd "$e2eRepo" && go build -o "$FROSTROOT" ./cmd/frostroot); then
			e2e_abort "go build failed"
		fi
	fi
	echo "binary: $("$FROSTROOT" version | head -1)"
}

# e2e_recipe DIR NAME RELEASE "PACKAGES" ["PYTHON PACKAGES" ["MORE TOML"]]
# writes DIR/frostroot.toml of $E2E_DISTRO for a student user on UTC, the
# way every scenario wants it, with the tables a scenario adds appended as
# they are.
e2e_recipe() {
	local dir=$1 name=$2 release=$3 packages=$4 python=${5:-} extra=${6:-}
	mkdir -p "$dir"
	{
		printf '[image]\nname = "%s"\n' "$name"
		if [ "$E2E_DISTRO" != ubuntu ]; then
			printf 'distro = "%s"\n' "$E2E_DISTRO"
		fi
		printf 'release = "%s"\narch = "amd64"\n' "$release"
		if [ "$E2E_TARGET" = disk ]; then
			printf 'target = "disk"\n'
		fi
		printf '\n[user]\nname = "student"\nsudo = true\n\n'
		if [ "$E2E_TARGET" != disk ]; then
			printf '[wsl]\nsystemd = true\ndefault_user = "student"\n\n'
		fi
		printf '[locale]\nlang = "en_US.UTF-8"\ntimezone = "UTC"\n\n'
		# The lists are split on spaces on purpose: one name per word.
		# shellcheck disable=SC2086
		printf '[packages]\ninclude = [%s]\n' "$(e2e_toml_list $packages)"
		if [ -n "$python" ]; then
			# shellcheck disable=SC2086
			printf '\n[python]\ninclude = [%s]\n' "$(e2e_toml_list $python)"
		fi
		if [ -n "$extra" ]; then
			printf '\n%s\n' "$extra"
		fi
	} > "$dir/frostroot.toml"
}

e2e_toml_list() {
	local list="" item
	for item in "$@"; do
		list+="${list:+, }\"$item\""
	done
	printf '%s' "$list"
}

# e2e_run LABEL DIR COMMAND... runs COMMAND in DIR with its output in
# $LAB/LABEL.out and $LAB/LABEL.err and its exit status in $e2eStatus.
# Standard input is $E2E_STDIN, /dev/null by default, so that nothing can
# sit waiting for an answer.
e2e_run() {
	local label=$1 dir=$2 started
	shift 2
	started=$(date +%s)
	echo "--- $label: $* (logs: $LAB/$label.out, .err)"
	(cd "$dir" && "$@") < "${E2E_STDIN:-/dev/null}" > "$LAB/$label.out" 2> "$LAB/$label.err"
	e2eStatus=$?
	echo "--- $label: exit $e2eStatus after $(($(date +%s) - started)) s"
}

# e2e_check DESCRIPTION COMMAND... counts a check, passed when COMMAND
# succeeds. The scenario goes on either way, so one run reports every
# failure.
e2e_check() {
	local description=$1
	shift
	e2eChecks=$((e2eChecks + 1))
	if "$@" > /dev/null 2>&1; then
		echo "  ok    $description"
	else
		echo "  FAIL  $description"
		e2eFailures=$((e2eFailures + 1))
	fi
}

# e2e_expect_status WANT LABEL checks the last e2e_run's exit status, and
# ends the scenario when it is wrong: whatever came next depended on it.
e2e_expect_status() {
	local want=$1 label=$2
	e2eChecks=$((e2eChecks + 1))
	if [ "$e2eStatus" -eq "$want" ]; then
		echo "  ok    $label exited $want"
		return 0
	fi
	echo "  FAIL  $label exited $e2eStatus, want $want; the end of its output:"
	if [ -s "$LAB/$label.err" ]; then
		tail -n 15 "$LAB/$label.err" | sed 's/^/        /'
	else
		tail -n 15 "$LAB/$label.out" | sed 's/^/        /'
	fi
	e2eFailures=$((e2eFailures + 1))
	e2e_end
}

# e2e_abort MESSAGE fails the scenario now.
e2e_abort() {
	echo "  FAIL  $*"
	e2eChecks=$((e2eChecks + 1))
	e2eFailures=$((e2eFailures + 1))
	e2e_end
}

# e2e_inconclusive MESSAGE ends the scenario with neither a pass nor a fail.
e2e_inconclusive() {
	echo "INCONCLUSIVE $e2eName: $*"
	exit 3
}

# e2e_end prints the verdict and exits with it.
e2e_end() {
	local seconds=$(($(date +%s) - e2eStarted))
	if [ "$e2eFailures" -eq 0 ]; then
		echo "PASS $e2eName: $e2eChecks checks in $seconds s"
		exit 0
	fi
	echo "FAIL $e2eName: $e2eFailures of $e2eChecks checks failed after $seconds s; the lab is $LAB"
	exit 1
}

# Predicates for e2e_check. The "lacks" ones insist that what they search
# exists: a file that was never written lacks everything, and a check that
# passes because its evidence is missing is worse than no check.
e2e_contains() { grep -qF -- "$2" "$1"; }                         # FILE TEXT
e2e_lacks() { [ -f "$1" ] && ! grep -qF -- "$2" "$1"; }           # FILE TEXT
e2e_has_line() { grep -qxF -- "$2" "$1"; }                        # FILE LINE, the whole line
e2e_matches() { grep -qE -- "$2" "$1"; }                          # FILE REGEX
e2e_lacks_match() { [ -f "$1" ] && ! grep -qE -- "$2" "$1"; }     # FILE REGEX
e2e_tree_lacks() { [ -d "$1" ] && ! grep -rqaF -- "$2" "$1"; }    # DIR TEXT, in any file under DIR
e2e_not() { ! "$@"; }                                             # COMMAND...
e2e_same_file() { cmp -s "$1" "$2"; }                             # FILE FILE
e2e_sha() { sha256sum "$1" 2> /dev/null | cut -d' ' -f1; }        # FILE: prints its SHA-256

# e2e_entries IMAGE lists the image's entries into a file in the lab, once
# per image, and prints the file's path: a tarball's as tar lists them, a
# disk image's root as e2e_disk_dump lays it out. The names start with ./
# When the image cannot be read there is no file, so every check against
# the list fails rather than passing on an empty one.
e2e_entries() {
	local list=$LAB/entries-$(basename "$1").txt dump
	if [ ! -s "$list" ] || [ "$1" -nt "$list" ]; then
		case "$1" in
		*.img)
			if dump=$(e2e_disk_dump "$1") && [ -d "$dump" ]; then
				(cd "$dump" && find . -mindepth 1 | sort) > "$list"
			else
				rm -f "$list"
				echo "  cannot list $1" >&2
			fi
			;;
		*)
			if ! tar -tzf "$1" > "$list" 2> /dev/null; then
				rm -f "$list"
				echo "  cannot list $1" >&2
			fi
			;;
		esac
	fi
	echo "$list"
}

# e2e_disk_root IMAGE copies the root partition out of a raw disk image into
# the lab, once per image, by what sfdisk says of its table, and prints the
# file's path. e2fsprogs' tools read it as the file system it is.
e2e_disk_root() {
	local image=$1 root=$LAB/root-of-$(basename "$1" .img).ext4 table start size
	if [ ! -s "$root" ] || [ "$image" -nt "$root" ]; then
		table=$(sfdisk --dump "$image" 2> /dev/null | grep 'name="root-x86-64"') || return 1
		start=$(printf '%s' "$table" | sed -n 's/.*start= *\([0-9]*\),.*/\1/p')
		size=$(printf '%s' "$table" | sed -n 's/.*size= *\([0-9]*\),.*/\1/p')
		[ -n "$start" ] && [ -n "$size" ] || return 1
		dd if="$image" of="$root" bs=512 skip="$start" count="$size" status=none || return 1
	fi
	echo "$root"
}

# e2e_disk_dump IMAGE dumps the root file system of a raw disk image into a
# directory in the lab, once per image, as this user, with debugfs, and
# prints the directory: enough to read any file, as e2e_unpack is for a
# tarball. Ownership and device nodes are not kept.
e2e_disk_dump() {
	local image=$1 dir=$LAB/dump-of-$(basename "$1" .img) root
	if [ ! -d "$dir" ] || [ "$image" -nt "$dir" ]; then
		root=$(e2e_disk_root "$image") || return 1
		rm -rf "$dir"
		mkdir -p "$dir"
		debugfs -R "rdump / $dir" "$root" > /dev/null 2>&1 || return 1
		touch "$dir"
	fi
	echo "$dir"
}

# e2e_unpack TARBALL DIR [MEMBER...] unpacks as this user, without device
# nodes and without ownership, which is enough to read any file and to point
# frostroot capture --root at it.
e2e_unpack() {
	local tarball=$1 dir=$2
	shift 2
	mkdir -p "$dir"
	tar --no-same-owner --exclude='./dev/*' -xzf "$tarball" -C "$dir" "$@" 2> /dev/null
	return 0
}

# e2e_certificate CN KEY CERT makes a self-signed certificate authority.
e2e_certificate() {
	openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=$1" \
		-addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
		-keyout "$2" -out "$3" > /dev/null 2>&1
}

# e2e_check_no_build_trust TARBALL checks that nothing the build trusted
# only for itself, a CA bundle or verification switched off, reached the
# image. apt's settings are the ones a mistake would ship: mmdebstrap puts
# every --aptopt in the image, which is why frostroot uses hooks.
e2e_check_no_build_trust() {
	local tarball=$1 entries apt
	entries=$(e2e_entries "$tarball")
	apt=$LAB/apt-of-$(basename "$tarball")
	case "$tarball" in
	*.img) apt=$(e2e_disk_dump "$tarball") ;;
	*) e2e_unpack "$tarball" "$apt" ./etc/apt ;;
	esac
	e2e_check "the image has an /etc/apt/apt.conf.d to look in" test -d "$apt/etc/apt/apt.conf.d"
	e2e_check "the image has no 99frostroot-build-ca" e2e_lacks "$entries" "99frostroot-build-ca"
	e2e_check "no file of the build's is left at the image's root" e2e_lacks_match "$entries" '^\./frostroot-'
	e2e_check "apt in the image names no CA bundle" e2e_tree_lacks "$apt/etc/apt" "CaInfo"
	e2e_check "apt in the image verifies TLS" e2e_tree_lacks "$apt/etc/apt" "Verify-Peer"
	e2e_check "apt in the image names nothing of this lab" e2e_tree_lacks "$apt/etc/apt" "$E2E_ROOT"
}

# e2e_check_no_fedora_build_trust TARBALL is e2e_check_no_build_trust for a
# Fedora image: the repository file, key and CA bundle frostroot hands mkosi
# are the package manager tree's, never the image's, and dnf in the image
# verifies TLS with its own store.
e2e_check_no_fedora_build_trust() {
	local tarball=$1 entries repos
	entries=$(e2e_entries "$tarball")
	repos=$LAB/repos-of-$(basename "$tarball")
	e2e_unpack "$tarball" "$repos" ./etc/yum.repos.d
	e2e_check "the image has an /etc/yum.repos.d to look in" test -d "$repos/etc/yum.repos.d"
	e2e_check "the image has no key or CA bundle of the build's" e2e_lacks_match "$entries" '^\./etc/frostroot-(keys|trust)'
	e2e_check "the image has no repository file of the build's" e2e_lacks "$entries" "./etc/yum.repos.d/mkosi.repo"
	e2e_check "dnf in the image names no CA bundle" e2e_tree_lacks "$repos/etc/yum.repos.d" "sslcacert"
	e2e_check "dnf in the image verifies TLS" e2e_tree_lacks "$repos/etc/yum.repos.d" "sslverify"
	e2e_check "dnf in the image names nothing of this lab" e2e_tree_lacks "$repos/etc/yum.repos.d" "$E2E_ROOT"
}

# e2e_remove PATH removes a path under the e2e root, and nothing else.
e2e_remove() {
	local target=$1
	case "$target" in
	"$E2E_ROOT" | "$E2E_ROOT"/?*) ;;
	*)
		echo "refusing to remove $target: it is not under $E2E_ROOT" >&2
		return 1
		;;
	esac
	[ -e "$target" ] || return 0
	chmod -R u+rwX "$target" 2> /dev/null
	rm -rf "$target" 2> /dev/null && return 0
	# An interrupted mmdebstrap can leave files owned by the subordinate
	# ids of its user namespace, and an unprivileged Fedora build's package
	# cache always is, which only that kind of namespace can remove.
	unshare --map-auto --map-root-user rm -rf "$target"
}
