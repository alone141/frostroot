package cli

import (
	"path/filepath"

	"frostroot/internal/builder"
	"frostroot/internal/recipe"
)

// insecureFlagUsage is what every command says of --insecure in its flag
// list; the usage text above the list says what it costs.
const insecureFlagUsage = "skip TLS certificate verification on every fetch, for a network whose certificate authority you do not have"

// The lines --insecure prints before a command does anything, on standard
// error like every warning. The first says what the flag gives up, the same
// way everywhere; the second says what the command still checks and what it
// does not, because that differs: apt's packages are signed and vendor's
// files are checksummed, but a resolve and a discovered key are protected by
// nothing but the connection.
const (
	insecureWarning = "warning: --insecure: TLS certificates are not verified on this run; anyone on the network path can answer as any server frostroot talks to.\n"

	insecureBuildDetail  = "The .deb packages are still checked against the archive's and each source's signatures.\n"
	insecurePythonDetail = "The Python packages are not: the resolve trusts whatever the network answers, and frostroot.lock will record it as unverified.\n"
	insecureVendorDetail = "Every file is still checked against the lock's SHA-256, so what lands in vendor/ is the lock's or nothing.\n"
	insecureFormDetail   = "Package names are only suggestions and build checks every one; a PPA's signing key, though, is fetched from wherever the network says, and every later build trusts it.\n"

	// unverifiedLockWarning is printed by every command that reads a lock
	// whose Python packages were resolved with --insecure, so that the risk
	// follows the lock rather than the person who passed the flag.
	unverifiedLockWarning = "warning: frostroot.lock resolved its Python packages without verifying TLS (frostroot build --insecure), so their hashes are only as trustworthy as that network was. Rebuild online with --ca-bundle, or on a trusted network, to replace them.\n"
)

// warnInsecure prints the flag's warning and the command's own detail lines.
func (a *App) warnInsecure(details ...string) {
	a.stderrf("%s", insecureWarning)
	for _, detail := range details {
		a.stderrf("%s", detail)
	}
}

// warnIfLockUnverified prints unverifiedLockWarning when the lock in the
// recipe directory says its Python packages were resolved with --insecure. A
// lock that cannot be read is nobody's concern here: whoever reads it for
// real reports that.
func (a *App) warnIfLockUnverified() {
	lock, err := recipe.LoadLock(filepath.Join(a.RecipeDir, builder.LockFileName))
	if err != nil {
		return
	}
	a.warnIfUnverified(lock)
}

// warnIfUnverified prints unverifiedLockWarning for a lock already loaded.
func (a *App) warnIfUnverified(lock recipe.Lockfile) {
	if lock.PythonResolvedUnverified() {
		a.stderrf("%s", unverifiedLockWarning)
	}
}
