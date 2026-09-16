package form

import "slices"

// Entry is one package the form offers by name.
type Entry struct {
	Category    string
	Name        string // the apt package name
	Description string // what it is for, in a few words
}

// catalog lists the packages init offers, grouped by category in the order
// they are shown. Every name exists in all supported Ubuntu releases; the
// integration test installs the whole catalog to prove it.
var catalog = []Entry{
	{"C/C++", "build-essential", "gcc, g++, make and the C library headers"},
	{"C/C++", "cmake", "cross-platform build system"},
	{"C/C++", "gdb", "the GNU debugger"},
	{"C/C++", "pkg-config", "compiler and linker flags for installed libraries"},
	{"C/C++", "clang", "the LLVM C/C++ compiler"},
	{"C/C++", "clang-format", "source code formatter"},
	{"C/C++", "valgrind", "memory error detector and profiler"},
	{"C/C++", "ninja-build", "small, fast build tool CMake can drive"},
	{"Python", "python3", "the interpreter"},
	{"Python", "python3-pip", "package installer (use a venv on 24.04)"},
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

// Catalog returns every entry in display order.
func Catalog() []Entry { return slices.Clone(catalog) }

// Categories returns the category names in display order.
func Categories() []string {
	var categories []string
	for _, entry := range catalog {
		if !slices.Contains(categories, entry.Category) {
			categories = append(categories, entry.Category)
		}
	}
	return categories
}

// InCatalog reports whether packageName is a catalog entry.
func InCatalog(packageName string) bool {
	return slices.ContainsFunc(catalog, func(entry Entry) bool { return entry.Name == packageName })
}

// SplitPackages divides a recipe's package list into the names the catalog
// offers and the rest, each in the order the list had them.
func SplitPackages(include []string) (catalogNames, otherNames []string) {
	for _, packageName := range include {
		if InCatalog(packageName) {
			catalogNames = append(catalogNames, packageName)
		} else {
			otherNames = append(otherNames, packageName)
		}
	}
	return catalogNames, otherNames
}

// catalogOptions renders the catalog as MultiSelect options.
func catalogOptions() []Option {
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
