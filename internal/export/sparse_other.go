//go:build !linux

package export

import (
	"context"
	"os"
)

// copyKeepingHoles copies the whole file: only Linux seeks holes, and only
// Linux builds.
func copyKeepingHoles(ctx context.Context, source, destination *os.File, size int64, report func(copied int64)) error {
	return copyWhole(ctx, source, destination, size, report)
}
