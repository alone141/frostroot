package recipe

import (
	"fmt"
	"regexp"

	"frostroot/internal/distro"
)

var (
	// The image name becomes a file name and a WSL distro name.
	imageNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
	userNameRe  = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	// Debian policy: at least two characters, lowercase, digits, + - and dot.
	// No = / or :, so versions and suites cannot be smuggled into the recipe.
	pkgTokenRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	// Locale and timezone are interpolated into mmdebstrap hooks in
	// internal/builder. Keep these strict; they are a shell-injection
	// boundary, not a cosmetic check. Neither may start with a dash.
	langRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*\.[A-Za-z0-9-]+$`)
	tzRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_+-]*(/[A-Za-z0-9][A-Za-z0-9_+-]*){0,2}$`)
)

// Validate returns every problem with the recipe, one message per problem.
// It needs no network and no root. Whether the timezone exists can only be
// checked inside the image, so build does that in a hook.
func Validate(r Recipe) []string {
	var probs []string
	if !imageNameRe.MatchString(r.Image.Name) {
		probs = append(probs, fmt.Sprintf("invalid image name %q (letters, digits, dot, dash, underscore; must not be empty)", r.Image.Name))
	}
	if _, err := distro.Lookup(r.Image.Release, r.Image.Arch); err != nil {
		probs = append(probs, err.Error())
	}
	if r.User.Name == "root" || !userNameRe.MatchString(r.User.Name) || len(r.User.Name) > 32 {
		probs = append(probs, fmt.Sprintf("invalid user name %q (lowercase letters, digits, - and _; 1-32 chars; not root)", r.User.Name))
	}
	if r.WSL.DefaultUser != "" && r.WSL.DefaultUser != r.User.Name {
		probs = append(probs, fmt.Sprintf("wsl.default_user %q must equal user.name %q", r.WSL.DefaultUser, r.User.Name))
	}
	if r.Locale.Lang != "" && !langRe.MatchString(r.Locale.Lang) {
		probs = append(probs, fmt.Sprintf("invalid locale lang %q (expected e.g. en_US.UTF-8)", r.Locale.Lang))
	}
	if r.Locale.Timezone != "" && !tzRe.MatchString(r.Locale.Timezone) {
		probs = append(probs, fmt.Sprintf("invalid timezone %q (expected e.g. UTC or Europe/Istanbul)", r.Locale.Timezone))
	}
	for _, p := range r.Packages.Include {
		if !pkgTokenRe.MatchString(p) {
			probs = append(probs, fmt.Sprintf("invalid package name %q (apt package names only; versions belong in the lock)", p))
		}
	}
	return probs
}

// DefaultUser is the account WSL logs in as.
func DefaultUser(r Recipe) string {
	if r.WSL.DefaultUser != "" {
		return r.WSL.DefaultUser
	}
	return r.User.Name
}
