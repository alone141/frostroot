package builder

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrRecipeDirBusy is returned when another build or vendor of the same
// user is running in the recipe directory: two of them place and read one
// lock, and interleaved they leave a lock beside a tarball it does not
// describe.
var ErrRecipeDirBusy = errors.New("another frostroot build or vendor is running in this recipe directory")

// maxHolderBytes bounds what is read of a hold file to name its holder.
const maxHolderBytes = 256

// RecipeDirHold keeps every other build and vendor of this user out of one
// recipe directory until it is released.
type RecipeDirHold struct {
	file *os.File
}

// Release lets the next build or vendor in. A nil hold, or one of a system
// that does not lock, releases nothing.
func (h *RecipeDirHold) Release() {
	if h == nil || h.file == nil {
		return
	}
	// Closing drops the lock. The file only names its holder, and a close
	// that lost that line loses nothing anyone needs.
	_ = h.file.Close()
}

// HoldRecipeDir takes the hold that a build keeps from before it first
// places anything, and vendor for its whole run. The hold is an advisory
// lock on a file under holdRoot, named for the resolved recipe directory:
// holdRoot is on the Linux filesystem even when the recipe sits on a
// Windows drive, where locking is doubtful, and the lock goes with the
// process however it ends. holdRoot is DefaultWorkRoot when "", whatever
// XDG_CACHE_HOME says, so that every run of this user holds one file
// whichever environment it was started from; tests name their own.
// Another user's run, or one under sudo, has a directory of its own and is
// not kept out. command names the run in the message the next one gets.
// Where frostroot does not build, nothing is held.
func HoldRecipeDir(getenv func(string) string, holdRoot, recipeDir, command string) (*RecipeDirHold, error) {
	if !recipeDirLocking {
		return &RecipeDirHold{}, nil
	}
	uid := os.Getuid()
	if holdRoot == "" {
		holdRoot = DefaultWorkRoot(uid)
	}
	holdRoot, err := prepareHoldRoot(getenv, holdRoot, uid)
	if err != nil {
		return nil, err
	}
	return holdRecipeDir(holdRoot, recipeDir, command)
}

// prepareHoldRoot prepares holdRoot as a work root is prepared and returns
// it. Only when that fails, on a read-only /var/tmp or under a directory
// someone else planted there, is the work root XDG_CACHE_HOME names used
// instead: such a host could not build without XDG_CACHE_HOME either, and
// it keeps working, but two runs of it with different XDG_CACHE_HOME are
// then not kept apart. When the work root is holdRoot, or cannot be used
// either, the error is holdRoot's, which a work root under /mnt would only
// hide.
func prepareHoldRoot(getenv func(string) string, holdRoot string, uid int) (string, error) {
	holdRootErr := prepareWorkRoot(holdRoot, uid)
	if holdRootErr == nil {
		return holdRoot, nil
	}
	workRoot, err := WorkRoot(getenv, uid)
	if err != nil || workRoot == holdRoot {
		return "", holdRootErr
	}
	if err := prepareWorkRoot(workRoot, uid); err != nil {
		return "", errors.Join(holdRootErr, err)
	}
	return workRoot, nil
}

// holdRecipeDir takes the hold under a hold root already prepared.
func holdRecipeDir(holdRoot, recipeDir, command string) (*RecipeDirHold, error) {
	if !recipeDirLocking {
		return &RecipeDirHold{}, nil
	}
	holdDir := filepath.Join(holdRoot, "holds")
	if err := os.Mkdir(holdDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("%w: %w", ErrBadWorkRoot, err)
	}
	resolvedDir := resolveRecipeDir(recipeDir)
	digest := sha256.Sum256([]byte(resolvedDir))
	holdPath := filepath.Join(holdDir, hex.EncodeToString(digest[:12])+".lock")
	file, held, err := lockExclusive(holdPath)
	if err != nil {
		return nil, fmt.Errorf("%w: locking %s: %w", ErrBadWorkRoot, holdPath, err)
	}
	if !held {
		holder := readHolder(holdPath)
		if holder != "" {
			holder = " (" + holder + ")"
		}
		return nil, fmt.Errorf("%w%s: %s; let it finish, then run this again", ErrRecipeDirBusy, holder, resolvedDir)
	}
	// Who holds it, for the message the next one gets. Best effort: the
	// lock is what keeps others out, not this line.
	if err := file.Truncate(0); err == nil {
		_, _ = file.WriteAt([]byte(fmt.Sprintf("frostroot %s, process %d", command, os.Getpid())), 0)
	}
	return &RecipeDirHold{file: file}, nil
}

// removeLeftovers removes what a build in recipeDir that was killed left
// behind: its temporary lock, and the tarball or image it had staged beside
// one of outputPaths, under the names export.CreateTemp gives them. Those
// are gigabytes on a Windows drive, and nothing else removes them. Only a
// run of this user in this directory makes them, and none is running: the
// caller holds the directory. A file another user than uid owns is a run
// of theirs, which the hold does not keep out, and stays. Best effort: what
// cannot be removed is in no build's way.
func removeLeftovers(recipeDir string, uid int, outputPaths ...string) {
	type leftoverName struct{ dir, prefix string }
	names := []leftoverName{{recipeDir, "." + LockFileName + "."}}
	for _, outputPath := range outputPaths {
		names = append(names, leftoverName{filepath.Dir(outputPath), "." + filepath.Base(outputPath) + "."})
	}
	for _, name := range names {
		entries, err := os.ReadDir(name.dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			number, isTemporary := strings.CutPrefix(entry.Name(), name.prefix)
			if isTemporary {
				number, isTemporary = strings.CutSuffix(number, ".tmp")
			}
			if !isTemporary || number == "" || strings.Trim(number, "0123456789") != "" || !entry.Type().IsRegular() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if owner, known := fileOwner(info); known && owner != uid {
				continue
			}
			_ = os.Remove(filepath.Join(name.dir, entry.Name()))
		}
	}
}

// resolveRecipeDir returns recipeDir absolute and with its symbolic links
// resolved, so that two paths to one directory share a hold. A directory
// that cannot be resolved is held by its absolute path.
func resolveRecipeDir(recipeDir string) string {
	absolute, err := filepath.Abs(recipeDir)
	if err != nil {
		absolute = recipeDir
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	return absolute
}

// readHolder returns what the hold file at path says of its holder, one
// line of it, or "" when it says nothing readable.
func readHolder(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data
	buffer := make([]byte, maxHolderBytes)
	count, _ := file.Read(buffer)
	holder, _, _ := strings.Cut(string(buffer[:count]), "\n")
	return strings.Map(lineSafe, strings.TrimSpace(holder))
}

// lineSafe drops control characters from what a hold file says, which any
// process of the user may have written.
func lineSafe(r rune) rune {
	if r < 0x20 || r == 0x7f {
		return -1
	}
	return r
}
