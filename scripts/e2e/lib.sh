# Shared by the scenarios in scripts/e2e. Source it; do not run it.
#
# Every scenario works under one root, so that one command removes all of it
# (scripts/e2e/clean.sh):
#
#   $FROSTROOT_E2E_ROOT, by default /var/tmp/frostroot-e2e-<uid>
#     cache/        XDG_CACHE_HOME for every frostroot run: its work root,
#                   cache/frostroot/build-*, and its package-index cache
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
e2eChecks=0
e2eFailures=0
e2eStatus=0

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

# e2e_require_bootstrap ends the scenario as SKIP unless a real build can
# run here: Linux, mmdebstrap and the Ubuntu archive keyring.
e2e_require_bootstrap() {
	e2e_require mmdebstrap
	if [ ! -e /usr/share/keyrings/ubuntu-archive-keyring.gpg ]; then
		echo "SKIP $e2eName: needs ubuntu-keyring (sudo apt install ubuntu-keyring)"
		exit 4
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

# e2e_recipe DIR NAME RELEASE "APT PACKAGES" ["PYTHON PACKAGES" ["MORE TOML"]]
# writes DIR/frostroot.toml for a student user on UTC, the way every
# scenario wants it, with the tables a scenario adds appended as they are.
e2e_recipe() {
	local dir=$1 name=$2 release=$3 packages=$4 python=${5:-} extra=${6:-}
	mkdir -p "$dir"
	{
		printf '[image]\nname = "%s"\nrelease = "%s"\narch = "amd64"\n\n' "$name" "$release"
		printf '[user]\nname = "student"\nsudo = true\n\n'
		printf '[wsl]\nsystemd = true\ndefault_user = "student"\n\n'
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

# e2e_entries TARBALL lists the tarball's entries into a file in the lab,
# once per tarball, and prints the file's path. The names start with ./
# When the tarball cannot be read there is no file, so every check against
# the list fails rather than passing on an empty one.
e2e_entries() {
	local list=$LAB/entries-$(basename "$1").txt
	if [ ! -s "$list" ] || [ "$1" -nt "$list" ]; then
		if ! tar -tzf "$1" > "$list" 2> /dev/null; then
			rm -f "$list"
			echo "  cannot list $1" >&2
		fi
	fi
	echo "$list"
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
	e2e_unpack "$tarball" "$apt" ./etc/apt
	e2e_check "the image has an /etc/apt/apt.conf.d to look in" test -d "$apt/etc/apt/apt.conf.d"
	e2e_check "the image has no 99frostroot-build-ca" e2e_lacks "$entries" "99frostroot-build-ca"
	e2e_check "no file of the build's is left at the image's root" e2e_lacks_match "$entries" '^\./frostroot-'
	e2e_check "apt in the image names no CA bundle" e2e_tree_lacks "$apt/etc/apt" "CaInfo"
	e2e_check "apt in the image verifies TLS" e2e_tree_lacks "$apt/etc/apt" "Verify-Peer"
	e2e_check "apt in the image names nothing of this lab" e2e_tree_lacks "$apt/etc/apt" "$E2E_ROOT"
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
	# ids of its user namespace, which only that kind of namespace can
	# remove.
	unshare --map-auto --map-root-user rm -rf "$target"
}
