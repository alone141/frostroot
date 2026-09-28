package distro_test

import (
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/distro"
	"frostroot/internal/pgp"
)

// TestFedoraKeysHoldOnlyTheirPinnedKey: dnf trusts any key in a gpgkey
// file, so a release's key file is only as trustworthy as every primary key
// in it. This test lives outside the package because distro imports no
// other frostroot package.
func TestFedoraKeysHoldOnlyTheirPinnedKey(t *testing.T) {
	for _, version := range distro.SupportedVersions(distro.Fedora) {
		release, err := distro.LookupFedora(version, distro.SupportedArch)
		if err != nil {
			t.Fatal(err)
		}
		key, err := pgp.ParsePublicKey(release.Key)
		if err != nil {
			t.Fatalf("Fedora %s: %v", version, err)
		}
		if !slices.Equal(key.Fingerprints, []string{release.KeyFingerprint}) {
			t.Errorf("Fedora %s's key file holds %q, want exactly the pinned %s", version, key.Fingerprints, release.KeyFingerprint)
		}
		if !key.Armored {
			t.Errorf("Fedora %s's key is not armored; dnf's gpgkey reads it as Fedora publishes it", version)
		}
	}
}

func TestFedoraReleasesInstallOverHTTPSFromTheirOwnRepositories(t *testing.T) {
	for _, version := range distro.SupportedVersions(distro.Fedora) {
		release, err := distro.LookupFedora(version, distro.SupportedArch)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, repository := range release.Repositories {
			ids = append(ids, repository.ID)
			for _, address := range []string{repository.Metalink, repository.BaseURL} {
				parsed, err := url.Parse(address)
				if err != nil || parsed.Scheme != "https" || parsed.User != nil {
					t.Errorf("Fedora %s, %s: %q is not a plain https URL", version, repository.ID, address)
				}
			}
			if strings.HasSuffix(repository.BaseURL, "/") {
				t.Errorf("Fedora %s, %s: the base URL %q ends in a slash; a location is joined to it with one", version, repository.ID, repository.BaseURL)
			}
		}
		if !slices.Equal(ids, []string{"fedora", "updates"}) {
			t.Errorf("Fedora %s's repositories are %q, want fedora then updates, the names dnf5 records", version, ids)
		}
	}
}

func TestLookupFedora(t *testing.T) {
	if _, err := distro.LookupFedora("44", "amd64"); err != nil {
		t.Errorf("LookupFedora(44, amd64) = %v", err)
	}
	// Both problems at once, release first, as Lookup reports them.
	_, err := distro.LookupFedora("39", "arm64")
	if !errors.Is(err, distro.ErrUnknownFedoraRelease) || !errors.Is(err, distro.ErrUnsupportedArch) {
		t.Errorf("LookupFedora(39, arm64) = %v, want both errors", err)
	}
	if err := distro.Check(distro.Fedora, "44", "amd64"); err != nil {
		t.Errorf("Check(Fedora, 44, amd64) = %v", err)
	}
	if err := distro.Check(distro.Fedora, "24.04", "amd64"); !errors.Is(err, distro.ErrUnknownFedoraRelease) || !strings.Contains(err.Error(), "known: 44") {
		t.Errorf("Check(Fedora, 24.04, amd64) = %v, want Fedora's releases named", err)
	}
	if got := distro.NewestVersion(distro.Fedora); got != "44" {
		t.Errorf("NewestVersion(Fedora) = %q", got)
	}
}
