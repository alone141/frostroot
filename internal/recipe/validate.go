package recipe

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"frostroot/internal/distro"
	"frostroot/internal/pgp"
	"frostroot/internal/pki"
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
	// pythonNamePattern is a PyPI distribution name as PEP 503 defines it.
	// The names reach a requirements file and pip's command line inside the
	// image, so, like the locale, this is an injection boundary: no version
	// specifier, extra, URL, path or space can pass it.
	pythonNamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	// pythonSeparators are the characters PEP 503 folds together when it
	// compares two distribution names.
	pythonSeparators = regexp.MustCompile(`[-_.]+`)
	// Source fields reach apt source lines and file names, so each is a
	// single token with no whitespace, brackets or slashes.
	sourceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	suitePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	componentPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*$`)
	// certificateNamePattern is the file name a certificate lands under in
	// /usr/local/share/ca-certificates. It reaches the provisioning script,
	// so it is an injection boundary like the locale.
	certificateNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// maxUserNameLength is the longest name useradd accepts.
const maxUserNameLength = 32

// MaxSourceNameLength keeps keyring file names short. Capture and the PPA
// namer hold to the same limit, so it is one constant rather than three.
const MaxSourceNameLength = 32

// maxPythonNameLength is the longest project name PyPI accepts.
const maxPythonNameLength = 100

// maxCertificateNameLength keeps certificate file names short.
const maxCertificateNameLength = 64

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
	seenPythonNames := map[string]bool{}
	for _, packageName := range imageRecipe.PythonPackages() {
		if err := CheckPythonPackageName(packageName); err != nil {
			addProblem(err)
			continue
		}
		normalized := NormalizePythonName(packageName)
		if seenPythonNames[normalized] {
			problems = append(problems, fmt.Sprintf("python.include names %s twice", normalized))
		}
		seenPythonNames[normalized] = true
	}
	seenCertificateNames := map[string]bool{}
	for _, certificatePath := range imageRecipe.CertificatePaths() {
		if err := CheckCertificatePath(certificatePath); err != nil {
			addProblem(err)
			continue
		}
		name := CertificateName(certificatePath)
		if seenCertificateNames[name] {
			problems = append(problems, fmt.Sprintf("certificates.include: two files would both install as %s.crt", name))
		}
		seenCertificateNames[name] = true
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
	if !sourceNamePattern.MatchString(source.Name) || len(source.Name) > MaxSourceNameLength {
		problems = append(problems, fmt.Errorf("invalid source name %q (lowercase letters, digits and dashes; 1-%d characters)", source.Name, MaxSourceNameLength))
	}
	if err := CheckSourceURL(source.URL); err != nil {
		problems = append(problems, err)
	}
	if source.Suite != "" && !suitePattern.MatchString(source.Suite) {
		problems = append(problems, fmt.Errorf("invalid suite %q (letters, digits, dot, dash, underscore)", source.Suite))
	}
	for _, component := range source.Components {
		if err := CheckComponent(component); err != nil {
			problems = append(problems, err)
		}
	}
	if err := CheckKeyPath(source.Key); err != nil {
		problems = append(problems, err)
	}
	return problems
}

// sourceURLMetacharacters are the characters a "deb" line cannot carry in a
// URL. Apt splits the line on whitespace — which for it includes the
// vertical tab and the form feed — reads options out of brackets, and takes
// "#" as a comment to the end of the line, wherever it sits. Any of them
// silently produces another source than the recipe names, or none at all.
//
// url.Parse already refuses the control characters here, so they are named
// for the reader and to keep the rule in one place rather than resting on
// net/url's policy; the space, the brackets and "#" are the ones it lets
// through. Percent-encoded forms such as %23 stay allowed: they reach apt
// still encoded, so the line stays one line.
const sourceURLMetacharacters = " \t\r\n\v\f[]#"

// CheckSourceURL reports why url cannot be an apt source URL, or nil: it
// must be http or https with a host and none of the characters that would
// break the "deb" line it lands in.
func CheckSourceURL(sourceURL string) error {
	parsed, err := url.Parse(sourceURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || strings.ContainsAny(sourceURL, sourceURLMetacharacters) {
		return fmt.Errorf("invalid source url %q (expected http or https with no space, bracket or #, such as https://download.docker.com/linux/ubuntu)", sourceURL)
	}
	return nil
}

// CheckComponent reports why component cannot be an apt component, or nil.
// Capture needs it one name at a time: it reads the components of several
// copies of one source, and a name it cannot express should be left out
// rather than spoil the rest.
func CheckComponent(component string) error {
	if !componentPattern.MatchString(component) {
		return fmt.Errorf("invalid component %q (lowercase letters, digits, dot, plus, dash)", component)
	}
	return nil
}

// CheckKeyPath reports why keyPath cannot name a key file in the recipe
// directory, or nil: it must be relative and stay inside the directory.
func CheckKeyPath(keyPath string) error {
	if !insideRecipeDirectory(keyPath) {
		return fmt.Errorf("invalid key path %q (a relative path inside the recipe directory, such as keys/docker.asc)", keyPath)
	}
	return nil
}

// insideRecipeDirectory reports whether value names a file the build may
// read from beside the recipe: relative, and not a way out of the directory.
func insideRecipeDirectory(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.ContainsAny(value, "\\\x00") {
		return false
	}
	cleaned := path.Clean(filepath.ToSlash(value))
	return !strings.HasPrefix(cleaned, "/") && !strings.HasPrefix(cleaned, "../") && cleaned != ".." && cleaned != "."
}

// CheckCertificatePath reports why certificatePath cannot name a certificate
// file in the recipe directory, or nil. Same rule as a source's key, and one
// more: the file's name becomes a file name inside the image, and reaches
// the provisioning script.
func CheckCertificatePath(certificatePath string) error {
	if !insideRecipeDirectory(certificatePath) {
		return fmt.Errorf("invalid certificate path %q (a relative path inside the recipe directory, such as certs/corp-root.pem)", certificatePath)
	}
	name := CertificateName(certificatePath)
	if !certificateNamePattern.MatchString(name) || len(name) > maxCertificateNameLength {
		return fmt.Errorf("invalid certificate file name %q in %q (letters, digits, dot, dash and underscore; 1-%d characters)", name, certificatePath, maxCertificateNameLength)
	}
	return nil
}

// CertificateName is what a certificate file is called in the image, without
// the .crt that update-ca-certificates requires: the file's base name
// without its extension, so certs/corp-root.pem becomes corp-root.
func CertificateName(certificatePath string) string {
	base := path.Base(path.Clean(filepath.ToSlash(certificatePath)))
	return strings.TrimSuffix(base, path.Ext(base))
}

// CertificateInstallFileName is the file name a certificate lands under in
// /usr/local/share/ca-certificates: the first certificate of a file keeps
// CertificateName plus .crt; later ones get -2, -3 and so on. index is
// zero-based inside that file.
func CertificateInstallFileName(certificatePath string, index int) string {
	name := CertificateName(certificatePath)
	if index == 0 {
		return name + ".crt"
	}
	return name + "-" + strconv.Itoa(index+1) + ".crt"
}

// CertificatePath returns the absolute path of a recipe's certificate file.
func CertificatePath(recipeDir, certificatePath string) string {
	return filepath.Join(recipeDir, filepath.FromSlash(certificatePath))
}

// CheckCertificateFiles checks that every certificate the recipe names
// exists under recipeDir and holds at least one certificate, one problem per
// failing file. Validate cannot do this: it has no directory.
func CheckCertificateFiles(recipeDir string, certificatePaths []string) []string {
	var problems []string
	takenFileNames := map[string]string{}
	for _, certificatePath := range certificatePaths {
		if CheckCertificatePath(certificatePath) != nil {
			continue // already reported by Validate
		}
		data, err := os.ReadFile(CertificatePath(recipeDir, certificatePath))
		if err != nil {
			problems = append(problems, fmt.Sprintf("certificate %s: %v", certificatePath, err))
			continue
		}
		certificates, err := pki.ParseCertificates(data)
		if err != nil {
			problems = append(problems, fmt.Sprintf("certificate %s: %v", certificatePath, err))
			continue
		}
		for index := range certificates {
			fileName := CertificateInstallFileName(certificatePath, index)
			if owner, taken := takenFileNames[fileName]; taken {
				problems = append(problems, fmt.Sprintf("certificate %s and %s would both install as %s", owner, certificatePath, fileName))
				continue
			}
			takenFileNames[fileName] = certificatePath
		}
	}
	return problems
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

// CheckPythonPackageName reports why name is not a PyPI distribution name, or
// nil. A version specifier, an extra such as requests[socks], a URL or a path
// all fail here rather than reaching pip, because the recipe says what to
// install and the lock says which version it was.
func CheckPythonPackageName(name string) error {
	if !pythonNamePattern.MatchString(name) || len(name) > maxPythonNameLength {
		return fmt.Errorf("invalid python package name %q (PyPI names only, 1-%d characters; versions belong in the lock)", name, maxPythonNameLength)
	}
	return nil
}

// NormalizePythonName returns name as PEP 503 compares it: lowercase, with
// every run of dots, dashes and underscores folded to one dash. PyPI treats
// Flask_SQLAlchemy, flask-sqlalchemy and Flask.SQLAlchemy as one project, and
// so must the recipe, the lock and everything that matches them up.
func NormalizePythonName(name string) string {
	return strings.ToLower(pythonSeparators.ReplaceAllString(name, "-"))
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
