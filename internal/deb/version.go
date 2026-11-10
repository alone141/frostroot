package deb

import (
	"strconv"
	"strings"
)

// CompareVersions orders two Debian package versions as dpkg does: by
// epoch, then by upstream version, then by Debian revision, the last two
// compared part by part, a run of non-digits and then a number, with "~"
// sorting before everything, the end of a string included, and letters
// before other characters. It returns a negative number when a is older
// than b, zero when they are the same version, and a positive number when
// a is newer. A version with no epoch has epoch 0 and one with no revision
// compares as revision 0, so 1.0, 0:1.0 and 1.0-0 are one version.
func CompareVersions(a, b string) int {
	epochA, upstreamA, revisionA := splitVersion(a)
	epochB, upstreamB, revisionB := splitVersion(b)
	if epochA != epochB {
		if epochA < epochB {
			return -1
		}
		return 1
	}
	if order := compareVersionPart(upstreamA, upstreamB); order != 0 {
		return order
	}
	return compareVersionPart(revisionA, revisionB)
}

// splitVersion splits [epoch:]upstream[-revision]. An epoch that is not a
// number counts as 0, as dpkg would refuse the version anyway.
func splitVersion(version string) (epoch int64, upstream, revision string) {
	upstream = version
	if before, after, hasEpoch := strings.Cut(version, ":"); hasEpoch {
		epoch, _ = strconv.ParseInt(before, 10, 64)
		upstream = after
	}
	if hyphen := strings.LastIndexByte(upstream, '-'); hyphen >= 0 {
		upstream, revision = upstream[:hyphen], upstream[hyphen+1:]
	}
	return epoch, upstream, revision
}

// compareVersionPart is dpkg's verrevcmp: alternate runs of non-digits,
// compared character by character by versionCharacterOrder, and runs of
// digits, compared as numbers.
func compareVersionPart(a, b string) int {
	at := func(text string, index int) byte {
		if index < len(text) {
			return text[index]
		}
		return 0
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			orderA, orderB := versionCharacterOrder(at(a, i)), versionCharacterOrder(at(b, j))
			if orderA != orderB {
				return orderA - orderB
			}
			i++
			j++
		}
		for i < len(a) && a[i] == '0' {
			i++
		}
		for j < len(b) && b[j] == '0' {
			j++
		}
		firstDifference := 0
		for i < len(a) && j < len(b) && isDigit(a[i]) && isDigit(b[j]) {
			if firstDifference == 0 {
				firstDifference = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		if i < len(a) && isDigit(a[i]) {
			return 1
		}
		if j < len(b) && isDigit(b[j]) {
			return -1
		}
		if firstDifference != 0 {
			return firstDifference
		}
	}
	return 0
}

// versionCharacterOrder is where a character sorts in a run of non-digits:
// "~" before the end of the string, the end before letters, letters before
// everything else.
func versionCharacterOrder(character byte) int {
	switch {
	case character == 0, isDigit(character):
		return 0
	case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z':
		return int(character)
	case character == '~':
		return -1
	default:
		return int(character) + 256
	}
}

func isDigit(character byte) bool { return character >= '0' && character <= '9' }
