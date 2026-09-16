package form

import (
	"fmt"
	"slices"
	"strings"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

// Values are the answers, by field key. Input and Select answers are strings,
// Confirm answers bools, MultiSelect answers []string.
type Values map[string]any

// String returns the string answer for key, or "".
func (v Values) String(key string) string {
	text, _ := v[key].(string)
	return text
}

// Bool returns the yes/no answer for key, or false.
func (v Values) Bool(key string) bool {
	answer, _ := v[key].(bool)
	return answer
}

// Strings returns the list answer for key, or nil.
func (v Values) Strings(key string) []string {
	list, _ := v[key].([]string)
	return list
}

// Clone returns a copy that shares no lists with v.
func (v Values) Clone() Values {
	cloned := make(Values, len(v))
	for key, value := range v {
		if list, isList := value.([]string); isList {
			cloned[key] = slices.Clone(list)
		} else {
			cloned[key] = value
		}
	}
	return cloned
}

// Defaults returns the answers init starts from: a lab image on the newest
// release, a student user with sudo, the host's timezone, systemd on, no
// packages.
func Defaults(host Host) Values {
	return Values{
		KeyImageName:     "lab",
		KeyRelease:       "24.04",
		KeyUserName:      "student",
		KeySudo:          true,
		KeyTimezone:      HostTimezone(host),
		KeyLocale:        "en_US.UTF-8",
		KeySystemd:       true,
		KeyPackages:      []string{},
		KeyOtherPackages: "",
	}
}

// FromRecipe returns the answers that describe imageRecipe, for edit.
// Packages in the catalog become selections; the rest go to the free-text
// field. The recipe's own package order is remembered so ToRecipe can keep it.
func FromRecipe(imageRecipe recipe.Recipe) Values {
	catalogNames, otherNames := SplitPackages(imageRecipe.Packages.Include)
	timezone := imageRecipe.Locale.Timezone
	if timezone == "" {
		timezone = "UTC"
	}
	locale := imageRecipe.Locale.Lang
	if locale == "" {
		locale = "en_US.UTF-8"
	}
	return Values{
		KeyImageName:       imageRecipe.Image.Name,
		KeyRelease:         imageRecipe.Image.Release,
		KeyUserName:        imageRecipe.User.Name,
		KeySudo:            imageRecipe.User.Sudo,
		KeyTimezone:        timezone,
		KeyLocale:          locale,
		KeySystemd:         imageRecipe.WSL.Systemd,
		KeyPackages:        catalogNames,
		KeyOtherPackages:   strings.Join(otherNames, " "),
		keyOriginalInclude: slices.Clone(imageRecipe.Packages.Include),
	}
}

// ToRecipe turns answers into a recipe. The caller still runs
// recipe.Validate on it before writing: the form checks each answer, this
// checks their combination.
func ToRecipe(values Values) recipe.Recipe {
	userName := values.String(KeyUserName)
	return recipe.Recipe{
		Image: recipe.Image{Name: values.String(KeyImageName), Release: values.String(KeyRelease), Arch: distro.SupportedArch},
		User:  recipe.User{Name: userName, Sudo: values.Bool(KeySudo)},
		WSL:   recipe.WSL{Systemd: values.Bool(KeySystemd), DefaultUser: userName},
		Locale: recipe.Locale{
			Lang:     values.String(KeyLocale),
			Timezone: values.String(KeyTimezone),
		},
		Packages: recipe.Packages{
			Include: MergePackages(values.Strings(KeyPackages), values.String(KeyOtherPackages), values.Strings(keyOriginalInclude)),
		},
	}
}

// Summary describes the answers in four lines, for the page before the
// recipe is written.
func Summary(values Values) string {
	sudo := "no sudo"
	if values.Bool(KeySudo) {
		sudo = "passwordless sudo"
	}
	systemd := "systemd off"
	if values.Bool(KeySystemd) {
		systemd = "systemd on"
	}
	packages := MergePackages(values.Strings(KeyPackages), values.String(KeyOtherPackages), values.Strings(keyOriginalInclude))
	packagesText := "none"
	if len(packages) > 0 {
		packagesText = strings.Join(packages, " ")
	}
	return strings.Join([]string{
		fmt.Sprintf("Image     %s, Ubuntu %s %s", values.String(KeyImageName), values.String(KeyRelease), distro.SupportedArch),
		fmt.Sprintf("User      %s, %s", values.String(KeyUserName), sudo),
		fmt.Sprintf("System    %s, %s, %s", values.String(KeyTimezone), values.String(KeyLocale), systemd),
		fmt.Sprintf("Packages  %s", packagesText),
	}, "\n")
}

// MergePackages returns the package list a recipe should carry: every
// selected catalog package and every name typed in otherPackages, once each.
// Names that were in originalInclude keep their original order, so editing a
// recipe without touching its packages leaves the list as it was; new names
// follow in selection order.
func MergePackages(selected []string, otherPackages string, originalInclude []string) []string {
	wanted := map[string]bool{}
	for _, packageName := range selected {
		wanted[packageName] = true
	}
	for _, packageName := range splitPackageList(otherPackages) {
		wanted[packageName] = true
	}
	merged := []string{}
	listed := map[string]bool{}
	add := func(packageName string) {
		if wanted[packageName] && !listed[packageName] {
			listed[packageName] = true
			merged = append(merged, packageName)
		}
	}
	for _, packageName := range originalInclude {
		add(packageName)
	}
	for _, packageName := range selected {
		add(packageName)
	}
	for _, packageName := range splitPackageList(otherPackages) {
		add(packageName)
	}
	return merged
}
