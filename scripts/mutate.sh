#!/usr/bin/env bash
# A mutation check: break the code on purpose, run the tests, put it back.
# A fix in this repository says "with X reverted, test Y fails" in its commit
# body, and says it only after running it; this is how it is run.
#
# usage:
#   scripts/mutate.sh --revert COMMIT [-- GO-TEST-ARGS...]
#       Undo what COMMIT changed in non-test Go files, applying its diff in
#       reverse to the tree as it is now, keep the tests, and run them. For
#       a fix, that is "with the fix reverted". Files COMMIT added are left
#       alone. Two things stop this from proving anything, and it says so:
#       a later change to the same lines (the diff no longer applies), and
#       a fix that added a function its own tests call (they no longer
#       compile). Break the fix by hand with --sed then, keeping the
#       function and emptying what it does.
#   scripts/mutate.sh --sed FILE SED-SCRIPT [-- GO-TEST-ARGS...]
#       Apply one sed script to one file and run the tests, for example
#       scripts/mutate.sh --sed internal/recipe/validate.go 's/if parsed.User != nil {/if false {/'
#
# GO-TEST-ARGS default to the packages of the mutated files. The files are
# copied first and restored however the run ends, Ctrl-C included, so
# uncommitted work in them survives.
#
# Exit status: 0 when the tests failed, which is the point: the mutation was
# caught. 1 when they passed: it survived, and nothing pins that behavior.
# 2 when the mutation did not compile or the command was wrong, which proves
# nothing either way.
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2

usage() {
	sed -n '2,/^set /{/^#/s/^# \{0,1\}//p}' "$0"
}

if ! command -v go > /dev/null 2>&1; then
	echo "scripts/mutate.sh: go is not on PATH; on Windows run it through scripts/wsl.sh" >&2
	exit 2
fi

mode=${1:-}
files=()
case "$mode" in
--revert)
	if [ "$#" -lt 2 ]; then
		usage >&2
		exit 2
	fi
	commit=$2
	shift 2
	if ! git rev-parse --verify --quiet "$commit^{commit}" > /dev/null; then
		echo "scripts/mutate.sh: no commit $commit" >&2
		exit 2
	fi
	while IFS= read -r file; do
		files+=("$file")
	done < <(git diff --name-only --diff-filter=M "$commit^" "$commit" -- '*.go' ':(exclude)*_test.go')
	if [ "${#files[@]}" -eq 0 ]; then
		echo "scripts/mutate.sh: $commit modified no non-test Go file; mutate by hand with --sed" >&2
		exit 2
	fi
	added=$(git diff --name-only --diff-filter=A "$commit^" "$commit" -- '*.go' ':(exclude)*_test.go')
	;;
--sed)
	if [ "$#" -lt 3 ]; then
		usage >&2
		exit 2
	fi
	files=("$2")
	expression=$3
	shift 3
	if [ ! -f "${files[0]}" ]; then
		echo "scripts/mutate.sh: no file ${files[0]}" >&2
		exit 2
	fi
	;;
-h | --help)
	usage
	exit 0
	;;
*)
	usage >&2
	exit 2
	;;
esac
if [ "${1:-}" = -- ]; then
	shift
fi
testArgs=("$@")
if [ "${#testArgs[@]}" -eq 0 ]; then
	while IFS= read -r directory; do
		testArgs+=("./$directory/")
	done < <(for file in "${files[@]}"; do dirname "$file"; done | sort -u)
fi

backup=$(mktemp -d "${TMPDIR:-/var/tmp}/frostroot-mutate.XXXXXX") || exit 2
for file in "${files[@]}"; do
	mkdir -p "$backup/$(dirname "$file")"
	cp -p "$file" "$backup/$file"
done
restore() {
	for file in "${files[@]}"; do
		cp -p "$backup/$file" "$file"
	done
	rm -rf "$backup"
}
trap restore EXIT
trap 'exit 130' INT TERM

if [ "$mode" = --revert ]; then
	# The commit's own diff, in reverse, rather than the files as they were
	# before it: later commits to the same files are not the mutation.
	patch=$(git show --format= "$commit" -- "${files[@]}")
	if ! git apply -R --check <<< "$patch" 2> "$backup/apply.err"; then
		echo "scripts/mutate.sh: what $commit changed no longer applies in reverse to these files:" >&2
		sed 's/^/  /' "$backup/apply.err" >&2
		echo "Mutate the lines by hand with --sed." >&2
		exit 2
	fi
	git apply -R <<< "$patch"
	echo "=== undid what $(git rev-parse --short "$commit") changed in: ${files[*]}"
	if [ -n "$added" ]; then
		echo "    left as they are, added by it: $(echo "$added" | tr '\n' ' ')"
	fi
else
	sed -i -e "$expression" "${files[0]}"
	if cmp -s "$backup/${files[0]}" "${files[0]}"; then
		echo "scripts/mutate.sh: the sed script changed nothing in ${files[0]}" >&2
		exit 2
	fi
fi
echo "=== the mutation"
for file in "${files[@]}"; do
	diff -u "$backup/$file" "$file" | sed '1,2d' | head -60
done

echo "=== go test ${testArgs[*]}"
output=$(go test "${testArgs[@]}" 2>&1)
status=$?
grep -E '^(--- FAIL|FAIL|ok|panic:)|\[build failed\]|\[setup failed\]' <<< "$output" | head -40

# Here-strings, not "echo | grep -q": grep -q stops at the first match, and
# under pipefail echo's SIGPIPE would turn a match into a miss.
if grep -qE '\[build failed\]|\[setup failed\]' <<< "$output"; then
	grep -E '\.go:[0-9]+:[0-9]+: ' <<< "$output" | head -10
	echo "MUTATION DID NOT COMPILE: that proves nothing about the tests"
	exit 2
fi
if [ "$status" -ne 0 ]; then
	echo "MUTATION CAUGHT: the tests above failed with it, as they should"
	exit 0
fi
echo "MUTATION SURVIVED: every test passed with it, so none of them pins this behavior"
exit 1
