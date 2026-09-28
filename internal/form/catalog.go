package form

import (
	"slices"

	"frostroot/internal/distro"
)

// Entry is one package the form offers by name.
type Entry struct {
	Category    string
	Name        string // the package name, as apt or dnf takes it
	Description string // what it is for, in a few words
}

// ubuntuCatalog lists the packages init offers for Ubuntu, grouped by
// category in the order they are shown. Every name exists in all supported
// Ubuntu releases, which TestIntegrationCatalogExistsInEveryRelease checks
// against each release's archive.
var ubuntuCatalog = []Entry{
	{"C/C++", "build-essential", "gcc, g++, make and the C library headers"},
	{"C/C++", "cmake", "cross-platform build system"},
	{"C/C++", "gdb", "the GNU debugger"},
	{"C/C++", "pkg-config", "compiler and linker flags for installed libraries"},
	{"C/C++", "clang", "the LLVM C/C++ compiler"},
	{"C/C++", "clang-format", "source code formatter"},
	{"C/C++", "valgrind", "memory error detector and profiler"},
	{"C/C++", "ninja-build", "small, fast build tool CMake can drive"},
	{"Python", "python3", "the interpreter"},
	{"Python", "python3-pip", "package installer (use a venv on 24.04 and later)"},
	{"Python", "python3-venv", "virtual environments"},
	{"Python", "ipython3", "interactive shell"},
	{"Version control", "git", "distributed version control"},
	{"Version control", "git-lfs", "large file storage for git"},
	{"Editors", "vim", "Vi IMproved"},
	{"Editors", "nano", "small, friendly editor"},
	{"Editors", "emacs-nox", "Emacs without X"},
	{"Tools", "curl", "transfer URLs from the command line"},
	{"Tools", "wget", "download files"},
	{"Tools", "htop", "interactive process viewer"},
	{"Tools", "tmux", "terminal multiplexer"},
	{"Tools", "tree", "directory listing as a tree"},
	{"Tools", "unzip", "extract zip archives"},
	{"Tools", "jq", "command-line JSON processor"},
	{"Tools", "rsync", "fast file synchronization"},
	{"Tools", "openssh-client", "ssh, scp and sftp"},
	{"Languages", "default-jdk", "Java development kit"},
	{"Languages", "nodejs", "JavaScript runtime"},
	{"Languages", "npm", "Node package manager"},
	{"Languages", "golang-go", "the Go compiler"},
	{"Languages", "rustc", "the Rust compiler"},
	{"Languages", "cargo", "the Rust package manager"},
}

// fedoraCatalog is the same list for Fedora, in the same categories, as
// Fedora names the packages: gcc, gcc-c++ and make where Ubuntu has
// build-essential, the -bin packages that put node and npm on PATH rather
// than node-24 and npm-24, and Java 25 rather than java-latest-openjdk,
// which is OpenJDK 27's early-access build on Fedora 44. venv is part of
// Fedora's python3 and has no package. Every name is a package in every
// Fedora release the table knows, which
// TestIntegrationFedoraCatalogExistsInEveryRelease checks against each
// release's own index.
var fedoraCatalog = []Entry{
	{"C/C++", "gcc", "the GNU C compiler, with make and the C library headers"},
	{"C/C++", "gcc-c++", "the GNU C++ compiler"},
	{"C/C++", "make", "GNU make"},
	{"C/C++", "cmake", "cross-platform build system"},
	{"C/C++", "gdb", "the GNU debugger"},
	{"C/C++", "pkgconf-pkg-config", "pkg-config: compiler and linker flags for installed libraries"},
	{"C/C++", "clang", "the LLVM C/C++ compiler"},
	{"C/C++", "clang-tools-extra", "clang-format, clang-tidy and clangd"},
	{"C/C++", "valgrind", "memory error detector and profiler"},
	{"C/C++", "ninja-build", "small, fast build tool CMake can drive"},
	{"Python", "python3", "the interpreter, with venv for virtual environments"},
	{"Python", "python3-pip", "package installer"},
	{"Python", "python3-ipython", "interactive shell"},
	{"Version control", "git", "distributed version control"},
	{"Version control", "git-lfs", "large file storage for git"},
	{"Editors", "vim-enhanced", "Vi IMproved"},
	{"Editors", "nano", "small, friendly editor"},
	{"Editors", "emacs-nw", "Emacs without X"},
	{"Tools", "curl", "transfer URLs from the command line"},
	{"Tools", "wget2-wget", "download files: wget, as wget2 provides it"},
	{"Tools", "htop", "interactive process viewer"},
	{"Tools", "tmux", "terminal multiplexer"},
	{"Tools", "tree", "directory listing as a tree"},
	{"Tools", "unzip", "extract zip archives"},
	{"Tools", "jq", "command-line JSON processor"},
	{"Tools", "rsync", "fast file synchronization"},
	{"Tools", "openssh-clients", "ssh, scp and sftp"},
	{"Languages", "java-25-openjdk-devel", "Java development kit, OpenJDK 25"},
	{"Languages", "nodejs24-bin", "JavaScript runtime: node, from Node.js 24"},
	{"Languages", "nodejs24-npm-bin", "Node package manager: npm and npx"},
	{"Languages", "golang", "the Go compiler"},
	{"Languages", "rust", "the Rust compiler"},
	{"Languages", "cargo", "the Rust package manager"},
}

// catalogOf returns a family's catalog itself, for reading; none for a
// family frostroot does not know.
func catalogOf(family distro.Family) []Entry {
	switch family {
	case distro.Ubuntu:
		return ubuntuCatalog
	case distro.Fedora:
		return fedoraCatalog
	}
	return nil
}

// Catalog returns every entry of a family's catalog in display order.
func Catalog(family distro.Family) []Entry { return slices.Clone(catalogOf(family)) }

// Categories returns the category names of a family's catalog in display
// order.
func Categories(family distro.Family) []string {
	var categories []string
	for _, entry := range catalogOf(family) {
		if !slices.Contains(categories, entry.Category) {
			categories = append(categories, entry.Category)
		}
	}
	return categories
}

// InCatalog reports whether packageName is an entry of a family's catalog.
func InCatalog(family distro.Family, packageName string) bool {
	return slices.ContainsFunc(catalogOf(family), func(entry Entry) bool { return entry.Name == packageName })
}

// SplitPackages divides a recipe's package list into the names a family's
// catalog offers and the rest, each in the order the list had them.
func SplitPackages(family distro.Family, include []string) (catalogNames, otherNames []string) {
	for _, packageName := range include {
		if InCatalog(family, packageName) {
			catalogNames = append(catalogNames, packageName)
		} else {
			otherNames = append(otherNames, packageName)
		}
	}
	return catalogNames, otherNames
}

// catalogOptions renders a family's catalog as MultiSelect options.
func catalogOptions(family distro.Family) []Option {
	catalog := catalogOf(family)
	options := make([]Option, 0, len(catalog))
	for _, entry := range catalog {
		options = append(options, Option{
			Value:       entry.Name,
			Label:       entry.Name + "  " + entry.Description,
			Description: entry.Category,
		})
	}
	return options
}
