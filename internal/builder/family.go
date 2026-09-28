package builder

import (
	"context"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

// familyBuild is the part of one build that depends on the recipe's family:
// for Ubuntu, mmdebstrap, apt and dpkg. Build calls its methods in the order
// they are declared, with checkAgainstLock for an offline build and
// composeLock for an online one.
type familyBuild interface {
	// preflight takes the build's work root and frozen instant, and checks
	// the host before any work directory exists.
	preflight(workRoot string, instant frozenInstant) error
	// bootstrap installs the image in workDir, reads what it installed, and
	// returns the path of the tarball it wrote there.
	bootstrap(ctx context.Context, workDir string) (tarballPath string, err error)
	// checkAgainstLock compares an offline build's image with the lock it
	// was rebuilt from.
	checkAgainstLock() (packageCounts, error)
	// composeLock returns the lock that records an online build's image.
	composeLock() (recipe.Lockfile, packageCounts, error)
}

// packageCounts is how many packages an image holds, for the Result.
type packageCounts struct {
	installed int // the family's own packages
	python    int // the virtual environment's; 0 without Python packages
}

// newFamilyBuild returns the part of a build that depends on imageRecipe's
// family, and for an offline build the plan it rebuilds from; everything a
// build will trust is read by then.
func (b *Builder) newFamilyBuild(imageRecipe recipe.Recipe, options Options, progress Progress) (familyBuild, *offlinePlan, error) {
	if _, err := distro.FamilyOf(imageRecipe.Image.Distro); err != nil {
		return nil, nil, err
	}
	// Ubuntu is the only family so far.
	return newUbuntuBuild(b.Bootstrapper, imageRecipe, options, progress)
}
