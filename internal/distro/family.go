package distro

import (
	"errors"
	"fmt"
	"strings"
)

// Family is a distribution family: what an image is built from, and which
// releases there are to build. A recipe and a lock name theirs in their
// distro field.
type Family string

// Ubuntu is the family frostroot has built from the start, and the one a
// recipe or a lock that names none belongs to.
const Ubuntu Family = "ubuntu"

// ErrUnknownFamily means a distro field names a family frostroot does not
// build.
var ErrUnknownFamily = errors.New("unknown distro")

// Families lists the families frostroot builds, in the order a form offers
// them.
func Families() []Family { return []Family{Ubuntu} }

// FamilyOf returns the family a recipe's or a lock's distro field names:
// Ubuntu when it names none, which every recipe and lock written before
// there was a second family does.
func FamilyOf(name string) (Family, error) {
	if name == "" {
		return Ubuntu, nil
	}
	for _, family := range Families() {
		if string(family) == name {
			return family, nil
		}
	}
	return "", unknownFamilyError(name)
}

// Check reports whether family builds version for arch. For Ubuntu the
// errors are Lookup's: ErrUnknownRelease and ErrUnsupportedArch, joined when
// both are wrong.
func Check(family Family, version, arch string) error {
	if family == Ubuntu {
		_, err := Lookup(version, arch)
		return err
	}
	return unknownFamilyError(string(family))
}

func unknownFamilyError(name string) error {
	known := make([]string, 0, len(Families()))
	for _, family := range Families() {
		known = append(known, string(family))
	}
	return fmt.Errorf("%w %q (known: %s)", ErrUnknownFamily, name, strings.Join(known, ", "))
}
