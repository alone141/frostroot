package form

import (
	"fmt"
	"slices"
	"strings"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
	"frostroot/internal/sources"
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

// Sources returns the source list stored under key, or nil.
func (v Values) Sources(key string) []recipe.Source {
	list, _ := v[key].([]recipe.Source)
	return list
}

// Clone returns a copy that shares no lists with v.
func (v Values) Clone() Values {
	cloned := make(Values, len(v))
	for key, value := range v {
		switch list := value.(type) {
		case []string:
			cloned[key] = slices.Clone(list)
		case []recipe.Source:
			cloned[key] = slices.Clone(list)
		default:
			cloned[key] = value
		}
	}
	return cloned
}

// Defaults returns the answers init starts from: a lab image on the newest
// release, a student user with sudo, the host's timezone, systemd on, no
// packages, no extra sources.
func Defaults(host Host) Values {
	return Values{
		KeyImageName:      "lab",
		KeyRelease:        "24.04",
		KeyUserName:       "student",
		KeySudo:           true,
		KeyTimezone:       HostTimezone(host),
		KeyLocale:         "en_US.UTF-8",
		KeySystemd:        true,
		KeyPackages:       []string{},
		KeyOtherPackages:  "",
		KeyPythonPackages: "",
		KeyCertificates:   "",
		KeySources:        []string{},
		KeyPPAs:           "",
	}
}

// FromRecipe returns the answers that describe imageRecipe, for edit.
// Packages in the catalog become selections; the rest go to the free-text
// field. Sources likewise: catalog entries become selections, PPAs go to
// the PPA field, and anything else is kept as it is. The recipe's own order
// is remembered so ToRecipe can keep it.
func FromRecipe(imageRecipe recipe.Recipe) Values {
	catalogNames, otherNames := SplitPackages(imageRecipe.Packages.Include)
	catalogSources, ppas := SplitSources(imageRecipe.Sources)
	simpleCertificates, exoticCertificates := splitCertificatePaths(imageRecipe.CertificatePaths())
	timezone := imageRecipe.Locale.Timezone
	if timezone == "" {
		timezone = "UTC"
	}
	locale := imageRecipe.Locale.Lang
	if locale == "" {
		locale = "en_US.UTF-8"
	}
	return Values{
		KeyImageName:            imageRecipe.Image.Name,
		KeyRelease:              imageRecipe.Image.Release,
		KeyUserName:             imageRecipe.User.Name,
		KeySudo:                 imageRecipe.User.Sudo,
		KeyTimezone:             timezone,
		KeyLocale:               locale,
		KeySystemd:              imageRecipe.WSL.Systemd,
		KeyPackages:             catalogNames,
		KeyOtherPackages:        strings.Join(otherNames, " "),
		KeyPythonPackages:       strings.Join(imageRecipe.PythonPackages(), " "),
		KeySources:              catalogSources,
		KeyPPAs:                 strings.Join(ppas, " "),
		KeyCertificates:         strings.Join(simpleCertificates, " "),
		keyOriginalInclude:      slices.Clone(imageRecipe.Packages.Include),
		keyOriginalSources:      slices.Clone(imageRecipe.Sources),
		keyOriginalRelease:      imageRecipe.Image.Release,
		keyOriginalCertificates: exoticCertificates,
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
		Sources:      mergeAnswerSources(values),
		Python:       pythonTable(values.String(KeyPythonPackages)),
		Certificates: certificatesTable(certificatePaths(values)),
	}
}

// certificatePaths returns every certificate the answers name: those the
// field could not hold, carried through, then those typed into it.
func certificatePaths(values Values) []string {
	return slices.Concat(values.Strings(keyOriginalCertificates), splitPackageList(values.String(KeyCertificates)))
}

// splitCertificatePaths divides certificate paths into those the free-text
// field can hold and those it cannot: a space or a comma in a path would
// split it, so such a path is carried through an edit rather than shown.
func splitCertificatePaths(paths []string) (simple, exotic []string) {
	for _, path := range paths {
		if strings.ContainsAny(path, " ,\t\n\r") {
			exotic = append(exotic, path)
		} else {
			simple = append(simple, path)
		}
	}
	return simple, exotic
}

// pythonTable returns the [python] table for an answer, or nil when it names
// no package: a recipe that asks for nothing keeps no table.
func pythonTable(answer string) *recipe.Python {
	packages := splitPackageList(answer)
	if len(packages) == 0 {
		return nil
	}
	return &recipe.Python{Include: packages}
}

// certificatesTable returns the [certificates] table for the paths, or nil
// when there are none: a recipe that names no authority keeps no table.
func certificatesTable(certificatePaths []string) *recipe.Certificates {
	if len(certificatePaths) == 0 {
		return nil
	}
	return &recipe.Certificates{Include: certificatePaths}
}

// releaseSuite returns the code name of a release, or the release text
// itself when it is not one frostroot knows; Validate reports that.
func releaseSuite(release string) string {
	if known, err := distro.Lookup(release, distro.SupportedArch); err == nil {
		return known.Suite
	}
	return release
}

// Summary describes the answers in a few lines, for the page before the
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
	sourcesText := "Ubuntu's archive only"
	if extra := mergeAnswerSources(values); len(extra) > 0 {
		var names []string
		for _, source := range extra {
			names = append(names, sources.Describe(source))
		}
		sourcesText = strings.Join(names, ", ")
	}
	lines := []string{
		fmt.Sprintf("Image     %s, Ubuntu %s %s", values.String(KeyImageName), values.String(KeyRelease), distro.SupportedArch),
		fmt.Sprintf("User      %s, %s", values.String(KeyUserName), sudo),
		fmt.Sprintf("System    %s, %s, %s", values.String(KeyTimezone), values.String(KeyLocale), systemd),
		fmt.Sprintf("Packages  %s", packagesText),
		fmt.Sprintf("Sources   %s", sourcesText),
	}
	if pythonPackages := splitPackageList(values.String(KeyPythonPackages)); len(pythonPackages) > 0 {
		lines = append(lines, fmt.Sprintf("Python    %s", strings.Join(pythonPackages, " ")))
	}
	if paths := certificatePaths(values); len(paths) > 0 {
		lines = append(lines, fmt.Sprintf("Certs     %s", strings.Join(paths, " ")))
	}
	return strings.Join(lines, "\n")
}

// SplitSources divides a recipe's sources into catalog entry names, PPAs
// as "owner/name", and the rest (hand-written sources), each in recipe
// order.
func SplitSources(recipeSources []recipe.Source) (catalogNames, ppas []string) {
	for _, source := range recipeSources {
		if _, isCatalog := sources.Lookup(source.Name); isCatalog {
			catalogNames = append(catalogNames, source.Name)
		} else if owner, name, isPPA := sources.PPAOf(source.URL); isPPA && source.Name == sources.PPA(owner, name).Name {
			ppas = append(ppas, owner+"/"+name)
		}
	}
	return catalogNames, ppas
}

func mergeAnswerSources(values Values) []recipe.Source {
	return MergeSources(values.Strings(KeySources), values.String(KeyPPAs), values.Sources(keyOriginalSources), releaseSuite(values.String(keyOriginalRelease)), releaseSuite(values.String(KeyRelease)))
}

// MergeSources returns the source list a recipe should carry: the
// hand-written sources of the original recipe, the selected catalog entries
// resolved for the release, and the PPAs typed, once each by name. Sources
// that were in the original recipe keep their place and their fields (a
// catalog entry the user edited by hand stays as edited); new ones follow.
// originalSuite is the code name those original sources were written for;
// an unedited catalog row is resolved again for releaseSuite.
func MergeSources(selected []string, ppasText string, original []recipe.Source, originalSuite, releaseSuite string) []recipe.Source {
	var wanted []recipe.Source
	for _, name := range selected {
		if entry, isCatalog := sources.Lookup(name); isCatalog {
			wanted = append(wanted, entry.Source(releaseSuite))
		}
	}
	for _, ppa := range splitPackageList(ppasText) {
		if owner, name, err := sources.ParsePPA(ppa); err == nil {
			wanted = append(wanted, sources.PPA(owner, name))
		}
	}
	wantedByName := map[string]bool{}
	for _, source := range wanted {
		wantedByName[source.Name] = true
	}
	var merged []recipe.Source
	listed := map[string]bool{}
	for _, source := range original {
		entry, isCatalog := sources.Lookup(source.Name)
		owner, name, isPPA := sources.PPAOf(source.URL)
		isRecognized := isCatalog || (isPPA && source.Name == sources.PPA(owner, name).Name)
		if (isRecognized && !wantedByName[source.Name]) || listed[source.Name] {
			continue // deselected, or a duplicate
		}
		if isCatalog && originalSuite != "" && source.Equal(entry.Source(originalSuite)) {
			listed[source.Name] = true
			merged = append(merged, entry.Source(releaseSuite))
			continue
		}
		listed[source.Name] = true
		merged = append(merged, source)
	}
	for _, source := range wanted {
		if !listed[source.Name] {
			listed[source.Name] = true
			merged = append(merged, source)
		}
	}
	return merged
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
