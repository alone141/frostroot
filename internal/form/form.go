// Package form describes the questions init and edit ask, independent of how
// they are shown. The full-screen interface and the line-by-line one both
// render Fields; the answers travel as Values and become a recipe through
// ToRecipe. Adding a question is adding a Field here and mapping its key in
// FromRecipe and ToRecipe.
package form

import (
	"errors"
	"fmt"
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
	Key         string // the Values key; stable, lowercase
	Page        string // fields with the same page are shown together
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
	// keyOriginalCertificates is not a field either: certificate paths the
	// free-text field cannot hold, because a space or a comma in them would
	// split them, carried through an edit untouched.
	keyOriginalCertificates = "original_certificates"
)

// The pages fields are grouped on, in order.
const (
	// PageCaptured comes first and holds only what capture has to say; a
	// page with no fields is not shown, so init and edit never see it.
	PageCaptured = "Captured"
	PageImage    = "Image"
	PageUser     = "User"
	PageSystem   = "System"
	PagePackages = "Packages"
	PageSources  = "Sources"
	PageTrust    = "Trust"
)

// Pages returns the page titles in order.
func Pages() []string {
	return []string{PageCaptured, PageImage, PageUser, PageSystem, PagePackages, PageSources, PageTrust}
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

// Fields returns every question, in the order they are asked.
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
			Key: KeyRelease, Page: PageImage, Kind: KindSelect,
			Title:   "Ubuntu release",
			Options: releaseOptions(),
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
			Key: KeyPackages, Page: PagePackages, Kind: KindMultiSelect, Filterable: true,
			Title:       "Packages",
			Description: "Space selects, Enter continues, / filters",
			Options:     catalogOptions(),
		},
		{
			Key: KeyOtherPackages, Page: PagePackages, Kind: KindSearch,
			Title:       "Other packages",
			Description: "type to search the release's archive, or a name; Space adds the row",
			Placeholder: "none",
			Validate:    checkPackageList,
			OpenIndex:   host.OpenIndex,
		},
		{
			Key: KeyPythonPackages, Page: PagePackages, Kind: KindSearch,
			Title:       "Python packages",
			Description: "type to search PyPI, or a name; installed into the image's virtual environment",
			Placeholder: "none",
			Validate:    checkPythonPackageList,
			OpenIndex:   host.OpenPythonIndex,
			Summaries:   true,
		},
		{
			Key: KeySources, Page: PageSources, Kind: KindMultiSelect,
			Title:       "Third-party apt sources",
			Description: "Repositories besides Ubuntu's archive; their signing keys are fetched and checked when the recipe is written",
			Options:     sourceOptions(),
		},
		{
			Key: KeyPPAs, Page: PageSources, Kind: KindInput,
			Title:       "Other PPAs",
			Description: "Launchpad PPAs as owner/name, separated by spaces or commas",
			Placeholder: "none",
			Validate:    checkPPAList,
		},
		{
			Key: KeyCertificates, Page: PageTrust, Kind: KindInput,
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
func checkPPAList(text string) error {
	for _, ppa := range splitPackageList(text) {
		if _, _, err := sources.ParsePPA(ppa); err != nil {
			return err
		}
	}
	return nil
}

// releaseOptions lists the supported Ubuntu releases, marking the one past
// its standard support.
func releaseOptions() []Option {
	var options []Option
	for _, version := range distro.SupportedVersions() {
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

// checkPackageList validates a free-text list of package names separated by
// spaces or commas. An empty list is fine.
func checkPackageList(text string) error {
	for _, packageName := range splitPackageList(text) {
		if err := recipe.CheckPackageName(packageName); err != nil {
			return err
		}
	}
	return nil
}

// checkPythonPackageList reports the first PyPI name in text that a recipe
// cannot hold.
func checkPythonPackageList(text string) error {
	for _, packageName := range splitPackageList(text) {
		if err := recipe.CheckPythonPackageName(packageName); err != nil {
			return err
		}
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
