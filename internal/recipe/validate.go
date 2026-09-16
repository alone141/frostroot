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
	if !imageNamePattern.MatchString(imageRecipe.Image.Name) {
		problems = append(problems, fmt.Sprintf("invalid image name %q (letters, digits, dot, dash, underscore; must not be empty)", imageRecipe.Image.Name))
	}
	if _, err := distro.Lookup(imageRecipe.Image.Release, imageRecipe.Image.Arch); err != nil {
		for _, lookupErr := range splitJoinedError(err) {
			problems = append(problems, lookupErr.Error())
		}
	}
	userName := imageRecipe.User.Name
	if userName == "root" || !userNamePattern.MatchString(userName) || len(userName) > maxUserNameLength {
		problems = append(problems, fmt.Sprintf("invalid user name %q (lowercase letters, digits, - and _; 1-%d chars; not root)", userName, maxUserNameLength))
	}
	if defaultUser := imageRecipe.WSL.DefaultUser; defaultUser != "" && defaultUser != userName {
		problems = append(problems, fmt.Sprintf("wsl.default_user %q must equal user.name %q", defaultUser, userName))
	}
	if lang := imageRecipe.Locale.Lang; lang != "" && !localePattern.MatchString(lang) {
		problems = append(problems, fmt.Sprintf("invalid locale lang %q (expected e.g. en_US.UTF-8)", lang))
	}
	if timezone := imageRecipe.Locale.Timezone; timezone != "" && !timezonePattern.MatchString(timezone) {
		problems = append(problems, fmt.Sprintf("invalid timezone %q (expected e.g. UTC or Europe/Istanbul)", timezone))
	}
	for _, packageName := range imageRecipe.Packages.Include {
		if !packageNamePattern.MatchString(packageName) {
			problems = append(problems, fmt.Sprintf("invalid package name %q (apt package names only; versions belong in the lock)", packageName))
		}
	}
	return problems
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
