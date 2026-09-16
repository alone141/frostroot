package recipe

import (
	"fmt"
	"regexp"

	"frostroot/internal/distro"
)

// Patterns for the values Validate checks. Locale and timezone are
// interpolated into the provisioning script in internal/builder, so those two
// are a shell-injection boundary, not a cosmetic check: keep them strict, and
// neither may start with a dash.
var (
	// imageNamePattern: the image name becomes a file name and a WSL
	// distribution name.
	imageNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
	userNamePattern  = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	// packageNamePattern follows Debian policy: at least two characters,
	// lowercase letters, digits, + - and dot. No = / or :, so versions and
	// suites cannot be smuggled into the recipe.
	packageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	localePattern      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*\.[A-Za-z0-9-]+$`)
	timezonePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_+-]*(/[A-Za-z0-9][A-Za-z0-9_+-]*){0,2}$`)
)

// maxUserNameLength is the longest name useradd accepts.
const maxUserNameLength = 32

// Validate returns every problem with imageRecipe, one message per problem,
// or nil when there are none. It needs no network and no root. Whether the
// timezone and locale exist can only be checked inside the image, so build
// does that during provisioning.
func Validate(imageRecipe Recipe) []string {
	var problems []string
	addProblem := func(err error) {
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	addProblem(CheckImageName(imageRecipe.Image.Name))
	if _, err := distro.Lookup(imageRecipe.Image.Release, imageRecipe.Image.Arch); err != nil {
		for _, lookupErr := range splitJoinedError(err) {
			addProblem(lookupErr)
		}
	}
	userName := imageRecipe.User.Name
	addProblem(CheckUserName(userName))
	if defaultUser := imageRecipe.WSL.DefaultUser; defaultUser != "" && defaultUser != userName {
		problems = append(problems, fmt.Sprintf("wsl.default_user %q must equal user.name %q", defaultUser, userName))
	}
	if lang := imageRecipe.Locale.Lang; lang != "" {
		addProblem(CheckLocale(lang))
	}
	if timezone := imageRecipe.Locale.Timezone; timezone != "" {
		addProblem(CheckTimezone(timezone))
	}
	for _, packageName := range imageRecipe.Packages.Include {
		addProblem(CheckPackageName(packageName))
	}
	return problems
}

// CheckImageName reports why name cannot be an image name, or nil. The name
// becomes a file name and a WSL distribution name.
func CheckImageName(name string) error {
	if !imageNamePattern.MatchString(name) {
		return fmt.Errorf("invalid image name %q (letters, digits, dot, dash, underscore; must not be empty)", name)
	}
	return nil
}

// CheckUserName reports why name cannot be the image's user, or nil.
func CheckUserName(name string) error {
	if name == "root" || !userNamePattern.MatchString(name) || len(name) > maxUserNameLength {
		return fmt.Errorf("invalid user name %q (lowercase letters, digits, - and _; 1-%d chars; not root)", name, maxUserNameLength)
	}
	return nil
}

// CheckLocale reports why lang is not an acceptable locale, or nil. Whether
// the locale exists can only be checked inside the image.
func CheckLocale(lang string) error {
	if !localePattern.MatchString(lang) {
		return fmt.Errorf("invalid locale lang %q (expected e.g. en_US.UTF-8)", lang)
	}
	return nil
}

// CheckTimezone reports why timezone is not an acceptable IANA zone name, or
// nil. Whether the zone exists can only be checked inside the image.
func CheckTimezone(timezone string) error {
	if !timezonePattern.MatchString(timezone) {
		return fmt.Errorf("invalid timezone %q (expected e.g. UTC or Europe/Istanbul)", timezone)
	}
	return nil
}

// CheckPackageName reports why name is not an apt package name, or nil.
func CheckPackageName(name string) error {
	if !packageNamePattern.MatchString(name) {
		return fmt.Errorf("invalid package name %q (apt package names only; versions belong in the lock)", name)
	}
	return nil
}

// splitJoinedError returns the errors combined by errors.Join, or err itself
// when it is not a joined error.
func splitJoinedError(err error) []error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	return []error{err}
}

// DefaultUser returns the account WSL logs in as.
func DefaultUser(imageRecipe Recipe) string {
	if imageRecipe.WSL.DefaultUser != "" {
		return imageRecipe.WSL.DefaultUser
	}
	return imageRecipe.User.Name
}
