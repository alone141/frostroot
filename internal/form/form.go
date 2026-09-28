// Package form describes the questions init and edit ask, independent of how
// they are shown. The full-screen interface and the line-by-line one both
// render Fields; the answers travel as Values and become a recipe through
// ToRecipe. Adding a question is adding a Field here and mapping its key in
// FromRecipe and ToRecipe.
package form

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
	"frostroot/internal/sources"
)

// Kind says how a field is answered.
type Kind int

// The kinds of field.
const (
	KindInput       Kind = iota // free text
	KindSelect                  // exactly one option
	KindMultiSelect             // any number of options
	KindConfirm                 // yes or no
	KindNote                    // text to read; nothing is answered
	// KindSearch is a list of package names, found by searching an index
	// or typed. Its answer is the text a KindInput would hold: names
	// separated by spaces. An interface that cannot search asks it as one.
	KindSearch
)

// NoteField returns a field that shows text on page and asks nothing: what
// capture found, before the questions it found it for.
func NoteField(page, title, text string) Field {
	return Field{Key: "note:" + page, Page: page, Kind: KindNote, Title: title, Description: text}
}

// Option is one choice of a Select or MultiSelect field.
type Option struct {
	Value       string // what the answer is, and what the recipe stores
	Label       string // how the choice is shown; Value when empty
	Description string // one line of explanation, or empty
}

// Field is one question.
type Field struct {
	Key  string // the Values key; stable, lowercase
	Page string // fields with the same page are shown together
	// Families are the families the field is asked for; none means every
	// one. Two fields may share a key when their families differ: each
	// family asks its release, and chooses its packages, its own way.
	Families    []distro.Family
	Title       string
	Description string // one line under the title, or empty
	Kind        Kind
	Options     []Option           // Select and MultiSelect
	Filterable  bool               // long lists: typing narrows the options
	Validate    func(string) error // Input: nil when the text is acceptable
	Placeholder string             // Input: hint shown while empty
	OpenIndex   IndexOpener        // Search: opens the index to search; nil means there is none
	// Summaries says this field's index looks descriptions up one name at
	// a time rather than publishing them. It is declared rather than
	// discovered so that the field is the same height before the index has
	// loaded as after.
	Summaries bool
	// Sourced says this field's index covers the apt sources the recipe
	// adds, so that it is opened again when they change. PyPI is one index
	// whatever the recipe adds, and a field that said otherwise would throw
	// away a download of it every time a source was ticked.
	Sourced bool
}

// AskedFor reports whether the field is a question of family.
func (f Field) AskedFor(family distro.Family) bool {
	return len(f.Families) == 0 || slices.Contains(f.Families, family)
}

// FieldsFor returns the questions of a family already chosen: the fields
// asked for it, without the question that chooses it. It is what the plain
// interface asks, and the full-screen one when the family is not a
// question: capture's, and init's with --distro.
func FieldsFor(fields []Field, family distro.Family) []Field {
	var asked []Field
	for _, field := range fields {
		if field.Key != KeyDistro && field.AskedFor(family) {
			asked = append(asked, field)
		}
	}
	return asked
}

// DisplayLabel returns the option's display text: Label, or Value when there
// is none.
func (o Option) DisplayLabel() string {
	if o.Label != "" {
		return o.Label
	}
	return o.Value
}

// The keys of the fields, which are also the keys of Values.
const (
	KeyImageName      = "image_name"
	KeyRelease        = "release"
	KeyUserName       = "user_name"
	KeySudo           = "sudo"
	KeyTimezone       = "timezone"
	KeyLocale         = "locale"
	KeySystemd        = "systemd"
	KeyPackages       = "packages"
	KeyOtherPackages  = "other_packages"
	KeyPythonPackages = "python_packages" // free text: PyPI names, separated by spaces or commas
	KeySources        = "sources"         // catalog source names
	KeyPPAs           = "ppas"            // free text: owner/name, separated by spaces or commas
	KeyCertificates   = "certificates"    // free text: PEM files beside the recipe, separated by spaces or commas
	// keyOriginalInclude is not a field: edit keeps the recipe's package
	// order here so an unchanged recipe is written back as it was.
	keyOriginalInclude = "original_include"
	// keyOriginalSources is not a field either: the recipe's sources as they
	// were, so hand-written ones survive an edit and the order is kept.
	keyOriginalSources = "original_sources"
	// keyOriginalRelease is the Ubuntu release those original sources were
	// written for, so an unedited catalog row can be resolved for a new one.
	keyOriginalRelease = "original_release"
	// keyOriginalCertificates is not a field either: certificate paths the
	// free-text field cannot hold, because a space or a comma in them would
	// split them, carried through an edit untouched.
	keyOriginalCertificates = "original_certificates"
	// keyPythonIndexURL is not a field either: [python] index_url is written
	// by hand, and the form must give it back unchanged rather than drop it
	// the first time someone runs frostroot edit.
	keyPythonIndexURL = "python_index_url"
	// KeyDistro is the family, such as "ubuntu": the full-screen form asks
	// it first thing, and the questions after it are that family's.
	KeyDistro = "distro"
	// keyOriginalDistro is not a field: [image] distro as the recipe wrote
	// it, so that a recipe which names its family keeps naming it, and one
	// that names none is written back without the line.
	keyOriginalDistro = "original_distro"
)

// The pages fields are grouped on, in order.
const (
	// PageCaptured comes first and holds only what capture has to say; a
	// page with no fields is not shown, so init and edit never see it.
	PageCaptured = "Captured"
	PageImage    = "Image"
	PageUser     = "User"
	PageSystem   = "System"
	// PageSources comes before PagePackages so that the packages of the
	// sources it adds are packages the picker can find: the index is opened
	// when the picker is reached, and it can only cover repositories the
	// answers already name. A recipe that adds none passes the page with
	// two keystrokes.
	PageSources  = "Sources"
	PagePackages = "Packages"
	PageTrust    = "Trust"
)

// Pages returns the page titles in order.
func Pages() []string {
	return []string{PageCaptured, PageImage, PageUser, PageSystem, PageSources, PagePackages, PageTrust}
}

// Host is what the form reads from the machine it runs on.
type Host struct {
	// ReadFile reads a file; os.ReadFile in production. It supplies the
	// timezone list and the host's own timezone.
	ReadFile func(name string) ([]byte, error)
	// RecipeDir is where the recipe lives, so that a field naming files
	// beside it can check them as they are typed; "" checks paths only.
	RecipeDir string
	// OpenIndex opens the package index of a release for the field that
	// searches it; nil means the form has none, and asks for names only.
	OpenIndex IndexOpener
	// OpenPythonIndex opens the PyPI project names for the field that
	// searches them; nil means the form has none. The release is passed and
	// ignored: PyPI is one index, whatever Ubuntu the image is.
	OpenPythonIndex IndexOpener
}

// The families of the fields only one family asks.
var (
	ubuntuOnly = []distro.Family{distro.Ubuntu}
	fedoraOnly = []distro.Family{distro.Fedora}
)

// Fields returns every question of every family, in the order they are
// asked: the family's own after the question that chooses it. FieldsFor
// picks one family's.
func Fields(host Host) []Field {
	return []Field{
		{
			Key: KeyImageName, Page: PageImage, Kind: KindInput,
			Title:       "Image name",
			Description: "Names the tarball and the WSL distribution",
			Placeholder: "lab",
			Validate:    recipe.CheckImageName,
		},
		{
			Key: KeyDistro, Page: PageImage, Kind: KindSelect,
			Title:   "Distribution",
			Options: familyOptions(),
		},
		{
			Key: KeyRelease, Page: PageImage, Kind: KindSelect, Families: ubuntuOnly,
			Title:   "Ubuntu release",
			Options: releaseOptions(distro.Ubuntu),
		},
		{
			Key: KeyRelease, Page: PageImage, Kind: KindSelect, Families: fedoraOnly,
			Title:   "Fedora release",
			Options: releaseOptions(distro.Fedora),
		},
		{
			Key: KeyUserName, Page: PageUser, Kind: KindInput,
			Title:       "User name",
			Description: "The account WSL logs in as; it owns /home/<name>",
			Placeholder: "student",
			Validate:    recipe.CheckUserName,
		},
		{
			Key: KeySudo, Page: PageUser, Kind: KindConfirm,
			Title:       "Passwordless sudo",
			Description: "Right for a lab image; a hardened server would say no",
		},
		{
			Key: KeyTimezone, Page: PageSystem, Kind: KindSelect, Filterable: true,
			Title: "Timezone",
			// The full-screen list starts in filter mode, where the filter
			// box takes the title's place, so the description names the
			// question too.
			Description: "Timezone: type to filter, for example \"ist\" for Europe/Istanbul, then Enter",
			Options:     timezoneOptions(host),
		},
		{
			Key: KeyLocale, Page: PageSystem, Kind: KindSelect,
			Title:   "Locale",
			Options: localeOptions(),
		},
		{
			Key: KeySystemd, Page: PageSystem, Kind: KindConfirm,
			Title:       "Boot with systemd",
			Description: "Needed for services, snapd and most tutorials",
		},
		{
			Key: KeySources, Page: PageSources, Kind: KindMultiSelect, Families: ubuntuOnly,
			Title:       "Third-party apt sources",
			Description: "Repositories besides Ubuntu's archive; their signing keys are fetched and checked when the recipe is written",
			Options:     sourceOptions(),
		},
		{
			Key: KeyPPAs, Page: PageSources, Kind: KindInput, Families: ubuntuOnly,
			Title:       "Other PPAs",
			Description: "Launchpad PPAs as owner/name, separated by spaces or commas",
			Placeholder: "none",
			Validate:    checkPPAList,
		},
		{
			Key: KeyPackages, Page: PagePackages, Kind: KindMultiSelect, Filterable: true, Families: ubuntuOnly,
			Title:       "Packages",
			Description: "Space selects, Enter continues, / filters",
			Options:     catalogOptions(distro.Ubuntu),
		},
		{
			Key: KeyOtherPackages, Page: PagePackages, Kind: KindSearch, Families: ubuntuOnly,
			Title:       "Other packages",
			Description: "type to search the archive and the sources above, or a name; Space adds",
			Placeholder: "none",
			Validate:    checkPackageList,
			OpenIndex:   host.OpenIndex,
			Sourced:     true,
		},
		{
			Key: KeyPythonPackages, Page: PagePackages, Kind: KindSearch, Families: ubuntuOnly,
			Title:       "Python packages",
			Description: "type to search PyPI, or a name; installed into the image's virtual environment",
			Placeholder: "none",
			Validate:    checkPythonPackageList,
			OpenIndex:   host.OpenPythonIndex,
			Summaries:   true,
		},
		// Fedora's packages come from Fedora's own repositories, and its
		// Python packages are Fedora's python3-* ones: there is no PyPI field.
		{
			Key: KeyPackages, Page: PagePackages, Kind: KindMultiSelect, Filterable: true, Families: fedoraOnly,
			Title:       "Packages",
			Description: "Space selects, Enter continues, / filters",
			Options:     catalogOptions(distro.Fedora),
		},
		{
			Key: KeyOtherPackages, Page: PagePackages, Kind: KindSearch, Families: fedoraOnly,
			Title:       "Other packages",
			Description: "type to search Fedora's packages, or a name; Space adds",
			Placeholder: "none",
			Validate:    checkRPMList,
			OpenIndex:   host.OpenIndex,
		},
		{
			Key: KeyCertificates, Page: PageTrust, Kind: KindInput, Families: ubuntuOnly,
			Title:       "Certificate authorities",
			Description: "PEM files next to the recipe, separated by spaces or commas. The image trusts them, and so does the build: what a network that inspects TLS needs",
			Placeholder: "none",
			Validate:    checkCertificateList(host),
		},
	}
}

// checkCertificateList validates a free-text list of certificate files:
// the path rule always, and, when the host says where the recipe is, that
// each file is there and holds a certificate. A private key pasted in
// place of one is worth saying before the summary, not by build.
func checkCertificateList(host Host) func(string) error {
	return func(text string) error {
		paths := splitPackageList(text)
		for _, path := range paths {
			if err := recipe.CheckCertificatePath(path); err != nil {
				return err
			}
		}
		if host.RecipeDir == "" {
			return nil
		}
		if problems := recipe.CheckCertificateFiles(host.RecipeDir, paths); len(problems) > 0 {
			return errors.New(problems[0])
		}
		return nil
	}
}

// sourceOptions renders the source catalog as MultiSelect options.
func sourceOptions() []Option {
	var options []Option
	for _, entry := range sources.Catalog() {
		options = append(options, Option{
			Value:       entry.Name,
			Label:       entry.Title + "  " + entry.Description,
			Description: entry.Category,
		})
	}
	return options
}

// checkPPAList validates a free-text list of PPAs. An empty list is fine.
// Two PPAs whose source names would be the same are refused here: the
// recipe holds one source per name, so the second would otherwise be
// dropped on the way to the summary without anything being said.
func checkPPAList(text string) error {
	byName := map[string]string{}
	for _, ppa := range splitPackageList(text) {
		owner, name, err := sources.ParsePPA(ppa)
		if err != nil {
			return err
		}
		sourceName := sources.PPA(owner, name).Name
		identity := owner + "/" + name
		if other, taken := byName[sourceName]; taken && other != identity {
			return fmt.Errorf("%s and %s would both be called %q; the recipe can hold only one, so keep one of them", other, identity, sourceName)
		}
		byName[sourceName] = identity
	}
	return nil
}

// familyOptions lists the families frostroot builds, in the order the
// table gives them.
func familyOptions() []Option {
	var options []Option
	for _, family := range distro.Families() {
		options = append(options, Option{Value: string(family), Label: family.Name()})
	}
	return options
}

// releaseOptions lists the releases of a family that frostroot builds,
// marking an Ubuntu one past its standard support.
func releaseOptions(family distro.Family) []Option {
	var options []Option
	for _, version := range distro.SupportedVersions(family) {
		if family != distro.Ubuntu {
			options = append(options, Option{Value: version, Label: family.Name() + " " + version})
			continue
		}
		release, err := distro.Lookup(version, distro.SupportedArch)
		if err != nil {
			continue // SupportedVersions only lists what Lookup knows
		}
		option := Option{Value: version, Label: fmt.Sprintf("Ubuntu %s LTS (%s)", version, release.Suite)}
		if release.EndOfLife {
			option.Description = "past standard support: security fixes only with Ubuntu Pro"
		}
		options = append(options, option)
	}
	return options
}

// checkPackageList validates a free-text list of apt package names
// separated by spaces or commas. An empty list is fine.
func checkPackageList(text string) error {
	return checkNames(text, recipe.CheckPackageName)
}

// checkRPMList is the same for Fedora's package names, which may hold
// capitals and underscores (NetworkManager, perl-File-Temp): the rule a
// Fedora recipe's are validated by.
func checkRPMList(text string) error {
	return checkNames(text, recipe.CheckRPMName)
}

// checkNames reports the first name in text that check refuses.
func checkNames(text string, check func(string) error) error {
	for _, packageName := range splitPackageList(text) {
		if err := check(packageName); err != nil {
			return err
		}
	}
	return nil
}

// checkPythonPackageList reports the first PyPI name in text that a recipe
// cannot hold, or two names that are one project. recipe.Validate compares
// names as PEP 503 does, so flask-sqlalchemy beside flask.sqlalchemy was
// refused after the form's confirmation, with nothing written and the
// answers gone; here the answer can still be changed.
func checkPythonPackageList(text string) error {
	spellings := map[string]string{}
	for _, packageName := range splitPackageList(text) {
		if err := recipe.CheckPythonPackageName(packageName); err != nil {
			return err
		}
		normalized := recipe.NormalizePythonName(packageName)
		if earlier, listed := spellings[normalized]; listed {
			if earlier == packageName {
				return fmt.Errorf("%s is listed twice", packageName)
			}
			return fmt.Errorf("%s and %s are one PyPI project; keep one of them", earlier, packageName)
		}
		spellings[normalized] = packageName
	}
	return nil
}

// splitPackageList splits package names on commas and whitespace, as people
// type them for apt install.
func splitPackageList(text string) []string {
	return strings.FieldsFunc(text, func(character rune) bool {
		return character == ',' || character == ' ' || character == '\t' || character == '\n' || character == '\r'
	})
}
