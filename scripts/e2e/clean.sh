#!/usr/bin/env bash
# Remove everything the end-to-end scenarios made: $FROSTROOT_E2E_ROOT, by
# default /var/tmp/frostroot-e2e-<uid>, which holds every lab, log and image
# and the work directories of the builds they ran. It says what is there and
# how big first. Nothing outside that root is touched.
#
# usage: scripts/e2e/clean.sh [--dry-run]
source "$(dirname "$0")/lib.sh"

case "${1:-}" in
"") dryRun=0 ;;
--dry-run) dryRun=1 ;;
-h | --help)
	sed -n '2,/^source /{/^#/s/^# \{0,1\}//p}' "$0"
	exit 0
	;;
*)
	echo "usage: scripts/e2e/clean.sh [--dry-run]" >&2
	exit 2
	;;
esac

if [ ! -e "$E2E_ROOT" ]; then
	echo "nothing to remove: $E2E_ROOT does not exist"
	exit 0
fi
du -sh "$E2E_ROOT"/* 2> /dev/null | sed 's/^/  /'
total=$(du -sh "$E2E_ROOT" 2> /dev/null | cut -f1)
if [ "$dryRun" -eq 1 ]; then
	echo "would remove $E2E_ROOT ($total)"
	exit 0
fi
e2e_remove "$E2E_ROOT" || exit 1
echo "removed $E2E_ROOT ($total)"
