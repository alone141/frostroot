#!/usr/bin/env bash
# The end-to-end scenarios: real builds, the real mmdebstrap, the real
# network. Each one checks its own results.
#
# usage: scripts/e2e/run.sh              list the scenarios
#        scripts/e2e/run.sh NAME...      run them, one after another
#        scripts/e2e/run.sh all          run every one: about two hours
#
# A scenario ends in PASS, FAIL, INCONCLUSIVE (the world moved under it,
# such as the archive publishing between two builds) or SKIP (a tool it
# needs is missing). This exits 0 only when every scenario it ran passed.
# Labs and logs stay under $FROSTROOT_E2E_ROOT, by default
# /var/tmp/frostroot-e2e-<uid>, until scripts/e2e/clean.sh removes them.
# scripts/e2e/README.md says what each is for and how to add one.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=${FROSTROOT_E2E_ROOT:-/var/tmp/frostroot-e2e-$(id -u)}

scenarios() {
	local file name
	for file in "$here"/*.sh; do
		name=$(basename "$file" .sh)
		case "$name" in
		lib | run | clean) ;;
		*) echo "$name" ;;
		esac
	done
}

header() { # FILE KEY: the value of the file's "# KEY: value" line
	sed -n "s/^# $2: //p" "$1" | head -n 1
}

case "${1:-}" in
"")
	echo "Scenarios; run them with scripts/e2e/run.sh NAME... (scripts/e2e/README.md has more):"
	for name in $(scenarios); do
		printf '\n  %s: %s\n' "$name" "$(header "$here/$name.sh" takes)"
		header "$here/$name.sh" proves | fold -s -w 72 | sed 's/^/      /'
	done
	exit 0
	;;
-h | --help)
	sed -n '2,/^set /{/^#/s/^# \{0,1\}//p}' "$0"
	exit 0
	;;
esac

mapfile -t known < <(scenarios)
names=("$@")
if [ "$*" = all ]; then
	names=("${known[@]}")
fi
for name in "${names[@]}"; do
	# Not "scenarios | grep -q": grep stops reading at the first match, and
	# under pipefail the listing's SIGPIPE would read as "not found".
	found=0
	for candidate in "${known[@]}"; do
		[ "$candidate" = "$name" ] && found=1
	done
	if [ "$found" -eq 0 ]; then
		echo "scripts/e2e/run.sh: no scenario \"$name\"; run it with no arguments for the list" >&2
		exit 2
	fi
done

mkdir -p "$root" && chmod 755 "$root"
results=()
worst=0
for name in "${names[@]}"; do
	log=$root/$name.log
	bash "$here/$name.sh" 2>&1 | tee "$log"
	status=${PIPESTATUS[0]}
	case "$status" in
	0) verdict=PASS ;;
	3) verdict=INCONCLUSIVE ;;
	4) verdict=SKIP ;;
	*) verdict=FAIL ;;
	esac
	results+=("$(printf '%-13s %-18s %s' "$verdict" "$name" "$log")")
	[ "$status" -eq 0 ] || worst=1
done
echo
echo "=== e2e"
printf '%s\n' "${results[@]}"
exit "$worst"
