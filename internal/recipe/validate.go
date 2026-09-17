package recipe

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"frostroot/internal/distro"
	"frostroot/internal/pgp"
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
	// Source fields reach apt source lines and file names, so each is a
	// single token with no whitespace, brackets or slashes.
	sourceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	suitePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	componentPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*$`)
)

// maxUserNameLength is the longest name useradd accepts.
const maxUserNameLength = 32

// maxSourceNameLength keeps keyring file names short.
const maxSourceNameLength = 32

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
	seenSourceNames := map[string]bool{}
	for index, source := range imageRecipe.Sources {
		for _, err := range CheckSource(source) {
			problems = append(problems, fmt.Sprintf("sources[%d]: %v", index, err))
		}
		if seenSourceNames[source.Name] {
			problems = append(problems, fmt.Sprintf("sources[%d]: name %q is used twice", index, source.Name))
		}
		seenSourceNames[source.Name] = true
	}
	return problems
}

// CheckSource returns every problem with a source's fields, or nil. Whether
// the key file exists and is a key is CheckSourceKeys' job, since it needs
// the recipe directory.
func CheckSource(source Source) []error {
	var problems []error
	if !sourceNamePattern.MatchString(source.Name) || len(source.Name) > maxSourceNameLength {
		problems = append(problems, fmt.Errorf("invalid source name %q (lowercase letters, digits and dashes; 1-%d characters)", source.Name, maxSourceNameLength))
	}
	if err := CheckSourceURL(source.URL); err != nil {
		problems = append(problems, err)
	}
	if source.Suite != "" && !suitePattern.MatchString(source.Suite) {
		problems = append(problems, fmt.Errorf("invalid suite %q (letters, digits, dot, dash, underscore)", source.Suite))
	}
	for _, component := range source.Components {
		if !componentPattern.MatchString(component) {
			problems = append(problems, fmt.Errorf("invalid component %q (lowercase letters, digits, dot, plus, dash)", component))
		}
	}
	if err := CheckKeyPath(source.Key); err != nil {
		problems = append(problems, err)
	}
	return problems
}

// CheckSourceURL reports why url cannot be an apt source URL, or nil: it
// must be http or https with a host and no whitespace, because it lands in a
// "deb" line.
func CheckSourceURL(sourceURL string) error {
	parsed, err := url.Parse(sourceURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || strings.ContainsAny(sourceURL, " \t\r\n[]") {
		return fmt.Errorf("invalid source url %q (expected http or https, such as https://download.docker.com/linux/ubuntu)", sourceURL)
	}
	return nil
}

// CheckKeyPath reports why keyPath cannot name a key file in the recipe
// directory, or nil: it must be relative and stay inside the directory.
func CheckKeyPath(keyPath string) error {
	cleaned := path.Clean(filepath.ToSlash(keyPath))
	if keyPath == "" || strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "../") || cleaned == ".." || cleaned == "." || filepath.IsAbs(keyPath) || strings.ContainsAny(keyPath, "\\\x00") {
		return fmt.Errorf("invalid key path %q (a relative path inside the recipe directory, such as keys/docker.asc)", keyPath)
	}
	return nil
}

// CheckSourceKeys checks that every source's key file exists under
// recipeDir and holds an OpenPGP public key, one problem per failing
// source. Validate cannot do this: it has no directory.
func CheckSourceKeys(recipeDir string, sources []Source) []string {
	var problems []string
	for _, source := range sources {
		if CheckKeyPath(source.Key) != nil {
			continue // already reported by Validate
		}
		keyPath := filepath.Join(recipeDir, filepath.FromSlash(source.Key))
		data, err := os.ReadFile(keyPath)
		if err != nil {
			problems = append(problems, fmt.Sprintf("source %s: key file %s: %v", source.Name, source.Key, err))
			continue
		}
		if _, err := pgp.ParsePublicKey(data); err != nil {
			problems = append(problems, fmt.Sprintf("source %s: key file %s: %v", source.Name, source.Key, err))
		}
	}
	return problems
}

// KeyPath returns the absolute path of a source's key file.
func KeyPath(recipeDir string, source Source) string {
	return filepath.Join(recipeDir, filepath.FromSlash(source.Key))
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
