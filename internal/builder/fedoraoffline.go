package builder

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"frostroot/internal/distro"
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// Directories of an offline Fedora build, in its work directory.
const (
	// fedoraLocalMirrorName holds the vendored packages staged as local
	// repositories: tools/<repository> for the tools tree and
	// image/<repository> for the image, each with only its own build's
	// packages, and each under its online name, which dnf5 records.
	fedoraLocalMirrorName = "repos"
	// fedoraOfflineCacheName is an offline build's own package cache, so
	// that nothing an earlier build left in the shared one can reach it.
	fedoraOfflineCacheName = "package-cache"
)

// Patterns for what a Fedora lock says about a package that reaches mkosi's
// configuration or dnf5's state: an offline build asks for every package by
// name-version.arch, one per line of mkosi.conf. The name is checked as a
// recipe's is.
var (
	fedoraLockArchPattern    = regexp.MustCompile(`^[a-z0-9_]+$`)
	fedoraLockVersionPattern = regexp.MustCompile(`^([0-9]+:)?[A-Za-z0-9._+~^]+-[A-Za-z0-9._+~^]+$`)
)

// planFedoraOffline loads the lock in recipeDir and checks that it still
// describes imageRecipe, a Fedora recipe: the same release, architecture and
// requested packages, the langpack of the recipe's locale among its
// packages, and the release's key as the lock recorded it. The user, sudo,
// timezone and systemd settings may differ, as on Ubuntu; the locale may
// too, as long as the lock holds its langpack.
func planFedoraOffline(recipeDir string, imageRecipe recipe.Recipe, release distro.FedoraRelease) (*offlinePlan, error) {
	lockPath := filepath.Join(recipeDir, LockFileName)
	lock, err := recipe.LoadLock(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w in %s; run frostroot build online first, then frostroot vendor", ErrNoLock, recipeDir)
	}
	if err != nil {
		return nil, err
	}
	// As on Ubuntu: a lock of another family is read no further.
	if lockFamily, err := distro.FamilyOf(lock.Distro); err != nil || lockFamily != distro.Fedora {
		return nil, fmt.Errorf("%w: distro %q in the lock, %q in the recipe; run frostroot build online, then frostroot vendor", ErrLockMismatch, lock.Distro, distro.Fedora)
	}
	entries, err := pool.Manifest(lock)
	if errors.Is(err, pool.ErrNoChecksums) {
		return nil, fmt.Errorf("%w; run frostroot build online, then frostroot vendor", err)
	}
	if err != nil {
		return nil, err
	}
	if err := checkFedoraLockPackages(lock, release); err != nil {
		return nil, err
	}
	var differences []string
	if lock.Release != imageRecipe.Image.Release {
		differences = append(differences, fmt.Sprintf("release %s in the lock, %s in the recipe", lock.Release, imageRecipe.Image.Release))
	}
	if lock.Arch != imageRecipe.Image.Arch {
		differences = append(differences, fmt.Sprintf("arch %s in the lock, %s in the recipe", lock.Arch, imageRecipe.Image.Arch))
	}
	if added, removed := setDifferences(lock.Requested, imageRecipe.Packages.Include); len(added)+len(removed) > 0 {
		if len(added) > 0 {
			differences = append(differences, "packages added to the recipe: "+strings.Join(added, ", "))
		}
		if len(removed) > 0 {
			differences = append(differences, "packages removed from the recipe: "+strings.Join(removed, ", "))
		}
	}
	// A locale is a package on Fedora, not a file provisioning makes.
	if langpack := fedoraLangpack(imageRecipe); langpack != "" && !slices.ContainsFunc(lock.Packages, func(locked recipe.LockPackage) bool { return locked.Name == langpack }) {
		differences = append(differences, fmt.Sprintf("locale %s needs %s, which the lock does not hold", imageRecipe.Locale.Lang, langpack))
	}
	// The key is frostroot's own, not a file beside the recipe, but a
	// frostroot with another key must not rebuild a lock the first one
	// wrote as if nothing had changed.
	keyDigest := sha256.Sum256(release.Key)
	for _, repository := range lock.Repositories {
		switch repository.KeySHA256 {
		case hex.EncodeToString(keyDigest[:]):
		case "":
			differences = append(differences, fmt.Sprintf("the lock records no key_sha256 for repository %s", repository.Name))
		default:
			differences = append(differences, fmt.Sprintf("Fedora %s's key is not the one the lock recorded for repository %s (lock %s, frostroot %s)",
				release.Version, repository.Name, shortDigest(repository.KeySHA256), shortDigest(hex.EncodeToString(keyDigest[:]))))
		}
	}
	if len(differences) > 0 {
		return nil, fmt.Errorf("%w: %s; run frostroot build online, then frostroot vendor", ErrLockMismatch, strings.Join(differences, "; "))
	}
	return &offlinePlan{
		lockPath: lockPath,
		lock:     lock,
		entries:  entries,
		poolDir:  filepath.Join(recipeDir, filepath.FromSlash(pool.RPMsDirName)),
	}, nil
}

// checkFedoraLockPackages checks what a Fedora lock says that an offline
// build hands mkosi and dnf5: every package's name, version, architecture
// and repository, which must be one of the release's; each image package's
// reason; and no name and architecture locked twice in the image or in the
// tools tree. A lock is a text file anyone can edit.
func checkFedoraLockPackages(lock recipe.Lockfile, release distro.FedoraRelease) error {
	for _, repository := range lock.Repositories {
		if !slices.ContainsFunc(release.Repositories, func(known distro.FedoraRepository) bool { return known.ID == repository.Name }) {
			return fmt.Errorf("%w: repository %q is not one of Fedora %s's", pool.ErrBadLock, repository.Name, release.Version)
		}
	}
	for _, part := range []struct {
		name     string
		packages []recipe.LockPackage
	}{{"image", lock.Packages}, {"tools tree", lock.Tools}} {
		if len(part.packages) == 0 {
			return fmt.Errorf("%w: it records no packages for the %s; run frostroot build online, then frostroot vendor", pool.ErrBadLock, part.name)
		}
		seen := map[string]bool{}
		for _, locked := range part.packages {
			if err := recipe.CheckRPMName(locked.Name); err != nil {
				return fmt.Errorf("%w: %s: %w", pool.ErrBadLock, part.name, err)
			}
			if !fedoraLockVersionPattern.MatchString(locked.Version) || !fedoraLockArchPattern.MatchString(locked.Arch) {
				return fmt.Errorf("%w: %s package %s has version %q and arch %q, which are not an rpm's", pool.ErrBadLock, part.name, locked.Name, locked.Version, locked.Arch)
			}
			if part.name == "image" && !slices.Contains(fedoraReasons, locked.Reason) {
				return fmt.Errorf("%w: package %s has reason %q, which is not one of dnf5's", pool.ErrBadLock, locked.Name, locked.Reason)
			}
			if _, found := lock.Repository(locked.Source); !found {
				return fmt.Errorf("%w: %s package %s comes from repository %q, which the lock does not describe", pool.ErrBadLock, part.name, locked.Name, locked.Source)
			}
			nameArch := locked.Name + "." + locked.Arch
			if seen[nameArch] {
				return fmt.Errorf("%w: the %s locks %s twice", pool.ErrBadLock, part.name, nameArch)
			}
			seen[nameArch] = true
		}
	}
	return nil
}

// fedoraPackageSpecs returns what an offline build asks dnf for: every
// locked package as name-version.arch, its epoch included, so that only
// that very package matches.
func fedoraPackageSpecs(packages []recipe.LockPackage) []string {
	specs := make([]string, 0, len(packages))
	for _, locked := range packages {
		specs = append(specs, locked.Name+"-"+locked.Version+"."+locked.Arch)
	}
	return specs
}

// renderFedoraReasons renders dnf5's packages.toml as dnf5 writes it, with
// the reasons the lock records: an offline build asks for every package by
// name, which dnf5 records as the user's choice. Entries are sorted by
// name.arch, byte by byte, as dnf5 sorts them, and the file ends with an
// empty line, as dnf5's does. The lock was checked by
// checkFedoraLockPackages, so no name needs quoting.
func renderFedoraReasons(packages []recipe.LockPackage) string {
	sorted := slices.Clone(packages)
	slices.SortFunc(sorted, func(a, b recipe.LockPackage) int {
		return strings.Compare(a.Name+"."+a.Arch, b.Name+"."+b.Arch)
	})
	var text strings.Builder
	text.WriteString("version = \"1.0\"\n\n[packages]\n")
	for _, locked := range sorted {
		fmt.Fprintf(&text, "\"%s.%s\" = {reason = \"%s\"}\n", locked.Name, locked.Arch, locked.Reason)
	}
	text.WriteString("\n")
	return text.String()
}

// fedoraStage is one local repository an offline build stages: a
// repository's packages for one of mkosi's two builds.
type fedoraStage struct {
	dir     string
	entries []pool.Entry
}

// fedoraStages returns the local repositories of an offline build under
// mirrorDir, one per repository of the release for the tools tree and one
// for the image, with the vendored files each holds. A repository neither
// build installs from is still staged, empty, since the repository file
// names every one.
func fedoraStages(offline *offlinePlan, release distro.FedoraRelease, mirrorDir string) (tools, image map[string]fedoraStage) {
	byFileName := map[string]pool.Entry{}
	for _, entry := range offline.entries {
		byFileName[entry.FileName] = entry
	}
	stagesOf := func(part string, packages []recipe.LockPackage) map[string]fedoraStage {
		stages := map[string]fedoraStage{}
		for _, repository := range release.Repositories {
			stage := fedoraStage{dir: filepath.Join(mirrorDir, part, repository.ID)}
			for _, locked := range packages {
				if locked.Source == repository.ID {
					stage.entries = append(stage.entries, byFileName[path.Base(locked.Filename)])
				}
			}
			stages[repository.ID] = stage
		}
		return stages
	}
	return stagesOf("tools", offline.lock.Tools), stagesOf("image", offline.lock.Packages)
}

// stageFedoraRepositories puts the vendored files into the stages' local
// repositories, hard-linked where it can, as a phase with a bytes bar, and
// returns their directories, which the bootstrapper indexes, and each
// build's map of repository to directory. Every directory is readable by
// anyone, whatever the umask, because an unprivileged mkosi reads them as a
// subordinate uid.
func stageFedoraRepositories(offline *offlinePlan, release distro.FedoraRelease, mirrorDir string, progress Progress) (dirs []string, toolsLocal, imageLocal map[string]string, err error) {
	progress.Report(ProgressEvent{Phase: PhasePrepareRepository, Kind: EventPhaseStarted})
	tools, image := fedoraStages(offline, release, mirrorDir)
	var totalBytes, doneBytes int64
	for _, stages := range []map[string]fedoraStage{tools, image} {
		for _, stage := range stages {
			totalBytes += pool.TotalSize(stage.entries)
		}
	}
	toolsLocal, imageLocal = map[string]string{}, map[string]string{}
	for _, part := range []struct {
		stages map[string]fedoraStage
		local  map[string]string
	}{{tools, toolsLocal}, {image, imageLocal}} {
		for _, repository := range release.Repositories {
			stage := part.stages[repository.ID]
			if err := mkdirAllReadable(stage.dir); err != nil {
				return nil, nil, nil, fmt.Errorf("preparing the local repositories: %w", err)
			}
			base := doneBytes
			err := pool.StageFiles(offline.poolDir, stage.entries, stage.dir, func(stagedBytes, _ int64) {
				progress.Report(ProgressEvent{Phase: PhasePrepareRepository, Kind: EventProgress, Done: base + stagedBytes, Total: totalBytes, Unit: UnitBytes})
			})
			if err != nil {
				return nil, nil, nil, fmt.Errorf("preparing the local repositories: %w", err)
			}
			doneBytes += pool.TotalSize(stage.entries)
			dirs = append(dirs, stage.dir)
			part.local[repository.ID] = stage.dir
		}
	}
	progress.Report(ProgressEvent{Phase: PhasePrepareRepository, Kind: EventPhaseFinished})
	return dirs, toolsLocal, imageLocal, nil
}
