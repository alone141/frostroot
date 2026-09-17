package capture

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Finding is what capture saw in one area that a recipe cannot carry.
type Finding struct {
	Area     string   // heading, such as "Modified configuration"
	Count    int      // how many things were found; 0 means nothing
	Examples []string // some or all of them, for the report
	Advice   string   // one sentence on what to do about it
	// Unavailable, when set, says why the area could not be checked at all.
	Unavailable string
}

// Areas of the report, in the order they are listed. Every capture reports
// every area, with "nothing found" where that is the case.
const (
	AreaThirdPartySources  = "Third-party apt sources"
	AreaThirdPartyPackages = "Packages from third-party sources"
	AreaUnsourcedPackages  = "Packages from no known source"
	AreaModifiedConfig     = "Modified configuration files"
	AreaAddedEtc           = "Files added to /etc"
	AreaOutsideApt         = "Software outside apt"
	AreaServices           = "Services and scheduled jobs"
	AreaOtherUsers         = "Other people"
	AreaHome               = "The user's home directory"
	AreaGroups             = "Extra groups"
)

// defaultGroups are the supplementary groups adduser gives every desktop
// user; being in them means nothing.
var defaultGroups = []string{"adm", "audio", "cdrom", "dialout", "dip", "floppy", "lxd", "netdev", "plugdev", "sudo", "users", "video"}

// secretDirectories are home entries that hold credentials and must never
// be copied into an image.
var secretDirectories = []string{".ssh", ".gnupg", ".aws", ".kube", ".docker", ".netrc", ".git-credentials", ".config/gh"}

// Where software outside apt tends to live, relative to the root.
var (
	usrLocalBinaryDirs = []string{"usr/local/bin", "usr/local/sbin", "usr/local/lib"}
	optDir             = "opt"
	systemPipGlob      = "usr/local/lib/python3*/dist-packages/*.dist-info"
	userPipGlob        = ".local/lib/python3*/site-packages/*.dist-info"
	npmGlobalDir       = "usr/local/lib/node_modules"
	snapDirs           = []string{"snap", "var/lib/snapd/snaps"}
	flatpakDir         = "var/lib/flatpak"
	// homeToolchains are managers that install whole toolchains outside apt.
	homeToolchains = map[string]string{
		".cargo/bin": "cargo binaries", "go/bin": "go install binaries", ".nvm": "nvm (Node versions)",
		".rustup": "rustup toolchains", ".pyenv": "pyenv (Python versions)", ".local/pipx": "pipx applications",
		"miniconda3": "Miniconda", "anaconda3": "Anaconda", ".sdkman": "SDKMAN",
	}
)

// thirdPartySourceFinding lists the apt sources that are not Ubuntu's and
// could not be carried into the recipe, with the reason. The carried ones
// are in the recipe and in the captured section of the report.
func thirdPartySourceFinding(left []leftSource) Finding {
	finding := Finding{
		Area:   AreaThirdPartySources,
		Advice: "These sources could not be carried into the recipe; build installs from the Ubuntu archive and the recipe's [[sources]] only. A source with a signing key file can be added to the recipe by hand.",
	}
	for _, source := range left {
		finding.Examples = append(finding.Examples, source.String())
	}
	finding.Count = len(finding.Examples)
	return finding
}

// thirdPartyPackageFindings lists requested packages that only a third-party
// source offers and that source was not carried into the recipe, and the
// ones no index offers at all. carriedHosts are the hosts of the sources the
// recipe now holds; their packages are ordinary requested packages.
func thirdPartyPackageFindings(requested []string, origins packageOrigins, carriedHosts map[string]bool) (thirdParty, unsourced Finding) {
	thirdParty = Finding{Area: AreaThirdPartyPackages, Advice: "Their source is not in the recipe, so build looks for them in the Ubuntu archive and fails at the download phase if they are not there."}
	unsourced = Finding{Area: AreaUnsourcedPackages, Advice: "Installed from a downloaded .deb or from a source since removed; build will not find them."}
	if !origins.hasIndexes {
		thirdParty.Unavailable = "apt has no package indexes under /var/lib/apt/lists (apt update never ran here), so package origins could not be checked."
		unsourced.Unavailable = thirdParty.Unavailable
		return thirdParty, unsourced
	}
	for _, name := range requested {
		hosts := origins.thirdParty[name]
		carried := slices.ContainsFunc(hosts, func(host string) bool { return carriedHosts[host] })
		switch {
		case origins.inUbuntu[name] || carried:
		case len(hosts) > 0:
			thirdParty.Examples = append(thirdParty.Examples, fmt.Sprintf("%s (%s)", name, strings.Join(hosts, ", ")))
		default:
			unsourced.Examples = append(unsourced.Examples, name)
		}
	}
	thirdParty.Count, unsourced.Count = len(thirdParty.Examples), len(unsourced.Examples)
	return thirdParty, unsourced
}

// modifiedConfigFinding lists edited package configuration files.
func modifiedConfigFinding(modified []string) Finding {
	return Finding{
		Area: AreaModifiedConfig, Count: len(modified), Examples: modified,
		Advice: "Edits to files a package owns are not part of a recipe; redo them after import, or keep them in a repository the students apply.",
	}
}

// addedEtcFinding lists regular files under /etc that no package owns and
// the system did not generate.
func (root systemRoot) addedEtcFinding(owned map[string]bool, haveOwnership bool) Finding {
	finding := Finding{Area: AreaAddedEtc, Advice: "Files added to /etc by hand are not part of a recipe."}
	if !haveOwnership {
		finding.Unavailable = "dpkg's file lists under /var/lib/dpkg/info could not be read, so ownership of /etc files could not be checked."
		return finding
	}
	var added []string
	_ = filepath.WalkDir(root.path("etc"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !entry.Type().IsRegular() {
			return nil // unreadable directories and non-files are simply not findings
		}
		relative := root.relativePath(path)
		if !owned[relative] && !isGeneratedEtcPath(relative) {
			added = append(added, relative)
		}
		return nil
	})
	slices.Sort(added)
	finding.Count, finding.Examples = len(added), added
	return finding
}

// outsideAptFinding lists software that apt did not install: /usr/local,
// /opt, language package managers, snaps and flatpaks, in the system and in
// the user's home. Only names are read, never contents.
func (root systemRoot) outsideAptFinding(homeDir string) Finding {
	var items []string
	for _, dir := range usrLocalBinaryDirs {
		items = append(items, root.regularFilesUnder(dir, 2)...)
	}
	for _, entry := range root.entryNames(optDir) {
		items = append(items, "/opt/"+entry)
	}
	for _, distInfo := range root.globNames(systemPipGlob) {
		items = append(items, "pip (system): "+distInfoName(distInfo))
	}
	for _, module := range root.entryNames(npmGlobalDir) {
		if module != "npm" && module != "corepack" && !strings.HasPrefix(module, ".") {
			items = append(items, "npm (global): "+module)
		}
	}
	for _, snap := range root.snapNames() {
		items = append(items, "snap: "+snap)
	}
	if _, err := os.Stat(root.path(flatpakDir + "/app")); err == nil {
		for _, app := range root.entryNames(flatpakDir + "/app") {
			items = append(items, "flatpak: "+app)
		}
	}
	if homeDir != "" {
		homeRelative := strings.TrimPrefix(root.relativePath(homeDir), "/")
		for _, distInfo := range root.globNames(homeRelative + "/" + userPipGlob) {
			items = append(items, "pip (user): "+distInfoName(distInfo))
		}
		for _, subdir := range sortedKeys(homeToolchains) {
			if _, err := os.Stat(filepath.Join(homeDir, filepath.FromSlash(subdir))); err == nil {
				items = append(items, fmt.Sprintf("%s (~/%s)", homeToolchains[subdir], subdir))
			}
		}
	}
	slices.Sort(items)
	items = slices.Compact(items)
	return Finding{
		Area: AreaOutsideApt, Count: len(items), Examples: items,
		Advice: "Not installed by apt, so not in the recipe. Python packages can be named in the recipe's [python] table, which installs them from PyPI into the image's virtual environment; the rest must be reinstalled after import.",
	}
}

// servicesFinding lists systemd units and cron jobs no package owns.
func (root systemRoot) servicesFinding(owned map[string]bool, haveOwnership bool) Finding {
	finding := Finding{Area: AreaServices, Advice: "Units and jobs added by hand are not part of a recipe."}
	var items []string
	for _, unit := range root.entryNames("etc/systemd/system") {
		if !strings.HasSuffix(unit, ".service") && !strings.HasSuffix(unit, ".timer") {
			continue
		}
		unitPath := "/etc/systemd/system/" + unit
		info, err := os.Lstat(root.path(unitPath))
		if err != nil || !info.Mode().IsRegular() {
			continue // symlinks are enablement wiring, not units of their own
		}
		if !haveOwnership || !owned[unitPath] {
			items = append(items, unitPath)
		}
	}
	for _, crontab := range root.entryNames("var/spool/cron/crontabs") {
		items = append(items, "crontab of "+crontab)
	}
	for _, job := range root.entryNames("etc/cron.d") {
		if jobPath := "/etc/cron.d/" + job; !haveOwnership || !owned[jobPath] {
			items = append(items, jobPath)
		}
	}
	slices.Sort(items)
	finding.Count, finding.Examples = len(items), items
	return finding
}

// otherUsersFinding lists human accounts besides the chosen one.
func otherUsersFinding(accounts []account, chosen string) Finding {
	var others []string
	for _, acct := range accounts {
		if acct.name != chosen {
			others = append(others, fmt.Sprintf("%s (uid %d)", acct.name, acct.uid))
		}
	}
	return Finding{Area: AreaOtherUsers, Count: len(others), Examples: others, Advice: "A recipe has one user; everyone else creates their own account after import."}
}

// homeFinding lists the dotfiles and directories at the top of the home
// directory, by name only, and calls out the ones that hold secrets.
func (root systemRoot) homeFinding(homeDir string) Finding {
	finding := Finding{Area: AreaHome, Advice: "Dotfiles and project files are not part of a recipe; keep them in a repository the students clone. The entries marked as secrets must never be copied into an image."}
	if homeDir == "" {
		finding.Unavailable = "no home directory was found for the user."
		return finding
	}
	entries, err := os.ReadDir(homeDir)
	if err != nil {
		finding.Unavailable = fmt.Sprintf("%s could not be listed: %v", root.relativePath(homeDir), err)
		return finding
	}
	var dotfiles []string
	otherEntries := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".") {
			otherEntries++
			continue
		}
		switch {
		case slices.Contains(secretDirectories, name):
			dotfiles = append(dotfiles, name+" (secrets: never copy)")
		case entry.IsDir():
			dotfiles = append(dotfiles, name+"/")
		default:
			dotfiles = append(dotfiles, name)
		}
	}
	if otherEntries > 0 {
		dotfiles = append(dotfiles, fmt.Sprintf("and %d other files and directories", otherEntries))
	}
	finding.Count, finding.Examples = len(entries), dotfiles
	return finding
}

// groupsFinding lists the user's groups beyond the defaults, such as docker.
func groupsFinding(groups []string, userName string) Finding {
	var extra []string
	for _, group := range groups {
		if group != userName && !slices.Contains(defaultGroups, group) {
			extra = append(extra, group)
		}
	}
	return Finding{Area: AreaGroups, Count: len(extra), Examples: extra, Advice: "Group memberships are not part of a recipe; add the user to them after import."}
}

// snapNames lists installed snaps: the mounted ones under /snap, or, when
// that is not there, the snap files snapd keeps ("code_123.snap" is "code").
func (root systemRoot) snapNames() []string {
	var names []string
	for _, entry := range root.entryNames(snapDirs[0]) {
		if entry != "bin" && entry != "README" {
			names = append(names, entry)
		}
	}
	if len(names) > 0 {
		return names
	}
	for _, entry := range root.entryNames(snapDirs[1]) {
		if strings.HasSuffix(entry, ".snap") {
			name, _, _ := strings.Cut(strings.TrimSuffix(entry, ".snap"), "_")
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// entryNames lists a directory under the root, sorted; nil when it cannot
// be read.
func (root systemRoot) entryNames(relativeDir string) []string {
	entries, err := os.ReadDir(root.path(relativeDir))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// languageTrees are subtrees of /usr/local/lib that pip and npm own; their
// contents are reported as packages, not as files.
var languageTrees = []string{"node_modules", "python3*"}

// regularFilesUnder lists regular files under relativeDir up to depth levels
// down, as root-relative paths, leaving the language package trees to their
// own collectors.
func (root systemRoot) regularFilesUnder(relativeDir string, depth int) []string {
	base := root.path(relativeDir)
	var files []string
	_ = filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relative, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		if entry.IsDir() {
			if relative != "." && strings.Count(relative, string(filepath.Separator)) >= depth {
				return filepath.SkipDir
			}
			for _, tree := range languageTrees {
				if matched, _ := filepath.Match(tree, entry.Name()); matched {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Type().IsRegular() {
			files = append(files, root.relativePath(path))
		}
		return nil
	})
	return files
}

// globNames returns the base names matching a glob under the root.
func (root systemRoot) globNames(pattern string) []string {
	matches, err := filepath.Glob(root.path(pattern))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, filepath.Base(match))
	}
	return names
}

// distInfoName turns "requests-2.32.3.dist-info" into "requests 2.32.3".
func distInfoName(distInfo string) string {
	name := strings.TrimSuffix(distInfo, ".dist-info")
	return strings.Replace(name, "-", " ", 1)
}

// sortedKeys returns a map's keys in order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
