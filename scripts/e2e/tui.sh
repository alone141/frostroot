#!/usr/bin/env bash
# proves: the full-screen form draws in a real terminal, searches the release's real archive and PyPI, or with E2E_DISTRO=fedora switches to Fedora and searches its repositories, and writes a recipe with what was picked
# needs: the network, python3
# takes: about 3 minutes, most of it fetching the indexes the first time
#
# tui_drive.py runs frostroot init in a pseudo-terminal and answers the two
# questions a real terminal answers, which script(1) does not; without them
# the form never draws. The screens it saw are in the lab's tui.out, which
# is the way to look at a change to the form without a person at the
# terminal. The golden frames in internal/tui cover how each screen renders;
# this covers the binary, the terminal and the real indexes together.
source "$(dirname "$0")/lib.sh"

e2e_begin tui
e2e_require python3
e2e_build_frostroot
mkdir -p "$LAB/lab"

e2e_run tui "$LAB/lab" python3 "$e2eRepo/scripts/e2e/tui_drive.py" --family "$E2E_DISTRO" "$FROSTROOT"
e2e_expect_status 0 tui
recipe=$LAB/lab/frostroot.toml
e2e_check "the recipe has the package picked in the picker" e2e_matches "$recipe" '^include = \[.*"jq"'
if [ "$E2E_DISTRO" = fedora ]; then
	e2e_check "the recipe is Fedora's" e2e_matches "$recipe" '^distro = "fedora"$'
	e2e_check "on the release the form offered" e2e_matches "$recipe" '^release = "44"$'
	e2e_check "and holds nothing only Ubuntu's recipes can" e2e_lacks_match "$recipe" '^\[(python|certificates)\]|^\[\[sources\]\]'
else
	e2e_check "and the one picked in the PyPI search" e2e_matches "$recipe" '^include = \[.*"requests"'
	e2e_check "and names no distribution, as before there was a choice" e2e_lacks_match "$recipe" '^distro ='
fi
e2e_run validate "$LAB/lab" "$FROSTROOT" validate
e2e_expect_status 0 validate
e2e_end
