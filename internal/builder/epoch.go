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
		if offline.lock.SourceDateEpoch > 0 {
			return frozenInstant{epoch: offline.lock.SourceDateEpoch, fromLock: true}, nil
		}
		return frozenInstant{epoch: now.Unix()}, nil
	}
	if value := getenv(SourceDateEpochVariable); value != "" {
		epoch, err := strconv.ParseInt(value, 10, 64)
		if err != nil || epoch <= 0 {
			return frozenInstant{}, fmt.Errorf("%w: %q is not a positive whole number of seconds since 1970; unset it, or set it to the instant the image is to be frozen at (an offline build ignores it and freezes at the lock's)", ErrBadSourceDateEpoch, value)
		}
		return frozenInstant{epoch: epoch}, nil
	}
	return frozenInstant{epoch: now.Unix()}, nil
}
