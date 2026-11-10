package builder

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// SourceDateEpochVariable is the environment variable of the
// reproducible-builds convention: the instant, in seconds since 1970, that a
// build clamps its timestamps to. An online build honors it; an offline build
// ignores it, because the lock's instant is the input.
const SourceDateEpochVariable = "SOURCE_DATE_EPOCH"

// ErrBadSourceDateEpoch means SOURCE_DATE_EPOCH in the environment is not a
// positive whole number of seconds.
var ErrBadSourceDateEpoch = errors.New("unusable " + SourceDateEpochVariable)

// frozenInstant is the instant a build freezes its image at, and whether it
// came from the lock.
type frozenInstant struct {
	epoch    int64 // seconds since 1970
	fromLock bool  // reused from the lock, so the tarball is byte-identical with any other offline build of it
}

// chooseFrozenInstant decides the instant an image is frozen at, before any
// work is done. Online it is SOURCE_DATE_EPOCH from the environment when set,
// otherwise now, and the lock records it. Offline it is the lock's, whatever
// the environment says, so that every offline build of one lock produces the
// same bytes; a lock from before frostroot 0.6 records none, and such a build
// freezes at now, like an online one, with a tarball that matches no other.
func chooseFrozenInstant(getenv func(string) string, offline *offlinePlan, now time.Time) (frozenInstant, error) {
	if offline != nil {
		if epoch := offline.lock.SourceDateEpoch; epoch > 0 {
			if laterThanTheClock(epoch, now) {
				return frozenInstant{}, fmt.Errorf("%w: the lock freezes the image at %s, later than this machine's clock, %s; a file the build makes would keep the clock's time rather than the lock's, and two rebuilds would differ: set the clock right, then build again",
					ErrBadSourceDateEpoch, formatEpoch(epoch), now.UTC().Format(time.DateTime+" UTC"))
			}
			return frozenInstant{epoch: epoch, fromLock: true}, nil
		}
		return frozenInstant{epoch: now.Unix()}, nil
	}
	if value := getenv(SourceDateEpochVariable); value != "" {
		epoch, err := strconv.ParseInt(value, 10, 64)
		if err != nil || epoch <= 0 {
			return frozenInstant{}, fmt.Errorf("%w: %q is not a positive whole number of seconds since 1970; unset it, or set it to the instant the image is to be frozen at (an offline build ignores it and freezes at the lock's)", ErrBadSourceDateEpoch, value)
		}
		if laterThanTheClock(epoch, now) {
			hint := ""
			if len(value) >= 13 {
				hint = " (it looks like milliseconds; the convention is seconds)"
			}
			return frozenInstant{}, fmt.Errorf("%w: %q is later than this machine's clock%s; the lock would record it, and every offline rebuild before that instant would date the files it makes by its own clock, so that two of them differ: unset it, or set it to an instant that has passed", ErrBadSourceDateEpoch, value, hint)
		}
		return frozenInstant{epoch: epoch}, nil
	}
	return frozenInstant{epoch: now.Unix()}, nil
}

// maxClockSkew is how far an instant may lie ahead of this machine's clock
// and still count as passed: two machines' clocks disagree by seconds.
const maxClockSkew = 5 * time.Minute

// laterThanTheClock reports whether epoch is still ahead of now. mmdebstrap
// dates no file later than the frozen instant, and leaves an earlier date
// alone, so the instant makes a build's own files the same in every build
// only once it has passed: before it, each file keeps the moment it was
// written.
func laterThanTheClock(epoch int64, now time.Time) bool {
	return epoch > now.Add(maxClockSkew).Unix()
}

// formatEpoch renders an instant as the summary does.
func formatEpoch(epoch int64) string {
	return time.Unix(epoch, 0).UTC().Format(time.DateTime + " UTC")
}
