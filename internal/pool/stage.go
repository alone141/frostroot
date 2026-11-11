package pool

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"frostroot/internal/deb"
)

// StageProgress is told, as files are staged, how many bytes of the total
// have been linked or copied.
type StageProgress func(doneBytes, totalBytes int64)

// Stage turns the verified files of poolDir into a flat apt repository at
// repositoryDir: every entry is hard-linked when the two directories share a
// filesystem and copied otherwise, made world-readable so that apt's
// unprivileged fetcher inside mmdebstrap's user namespace can read it, and
// then Packages and Release are written. repositoryDir is created 0755. The
// caller verifies the pool first, and apt trusts the index, so the index is
// written only when every staged file still has its entry's SHA-256: a pool
// file replaced since the check fails with deb.ErrDigestMismatch instead of
// being installed.
func Stage(poolDir string, entries []Entry, repositoryDir string, release deb.FlatRelease, onProgress StageProgress) error {
	if err := os.MkdirAll(repositoryDir, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(repositoryDir, 0o755); err != nil {
		return err
	}
	if release.Date.IsZero() {
		release.Date = time.Now()
	}
	totalBytes := TotalSize(entries)
	var doneBytes int64
	fileNames := make([]string, 0, len(entries))
	wantSHA256 := make(map[string]string, len(entries))
	for _, entry := range entries {
		source := filepath.Join(poolDir, entry.FileName)
		destination := filepath.Join(repositoryDir, entry.FileName)
		if err := linkOrCopy(source, destination); err != nil {
			return fmt.Errorf("staging %s: %w", entry.FileName, err)
		}
		doneBytes += entry.Size
		if onProgress != nil {
			onProgress(doneBytes, totalBytes)
		}
		fileNames = append(fileNames, entry.FileName)
		wantSHA256[entry.FileName] = entry.SHA256
	}
	if err := deb.WriteFlatRepository(repositoryDir, release, fileNames, wantSHA256); err != nil {
		return fmt.Errorf("writing the repository index: %w", err)
	}
	return nil
}

// StageFiles puts the verified files of poolDir into destinationDir, which
// it creates 0755, without writing any index: an offline build hands the
// directory to pip, which needs the wheels and nothing else. As in Stage,
// each file is hard-linked when it can be and copied otherwise, and the
// caller verifies the pool first.
func StageFiles(poolDir string, entries []Entry, destinationDir string, onProgress StageProgress) error {
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(destinationDir, 0o755); err != nil {
		return err
	}
	totalBytes := TotalSize(entries)
	var doneBytes int64
	for _, entry := range entries {
		source := filepath.Join(poolDir, entry.FileName)
		if err := linkOrCopy(source, filepath.Join(destinationDir, entry.FileName)); err != nil {
			return fmt.Errorf("staging %s: %w", entry.FileName, err)
		}
		doneBytes += entry.Size
		if onProgress != nil {
			onProgress(doneBytes, totalBytes)
		}
	}
	return nil
}

// linkOrCopy makes destination hold the content of source: a hard link when
// the filesystem allows and the source is already world-readable, a copy
// otherwise. A link shares the source's mode, and changing that would change
// the user's pool file too, so a source with another mode is copied. Either
// way the result is mode 0644 and any earlier destination is replaced. A
// source that is a symbolic link stages what it points to, as Verify checked
// it: a hard link to the link itself would dangle, or point elsewhere, from
// the destination's directory. A source that is gone since the caller
// verified it, cannot be opened, or is not a regular file is ErrPoolRead. The
// last is checked up front, not by wrapping the reader, which would turn off
// io.Copy's kernel-side copy. An error of the destination, or an I/O error in
// the middle of the copy, is returned as it is.
func linkOrCopy(source, destination string) error {
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPoolRead, err)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPoolRead, err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrPoolRead, source)
	}
	if sourceInfo.Mode().Perm() == 0o644 {
		if err := os.Link(source, destination); err == nil {
			return nil
		}
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPoolRead, err)
	}
	defer func() { _ = sourceFile.Close() }() // read-only: closing cannot lose data
	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destinationFile, sourceFile); err != nil {
		_ = destinationFile.Close() // the copy failed; that error is the one returned
		return err
	}
	if err := destinationFile.Close(); err != nil {
		return err
	}
	return os.Chmod(destination, 0o644)
}
