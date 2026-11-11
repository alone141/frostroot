package builder

import (
	"context"
	"reflect"
	"testing"

	"frostroot/internal/recipe"
)

// TestOfflineBuildInstallsTheKeysAndCertificatesItChecked: an offline build
// compared the key and certificate files with the lock, then read them a
// second time, and the second read is what it staged and installed. A file
// rewritten between the two reads shipped although the check had passed.
// Nothing can rewrite a file between two reads inside newUbuntuBuild from
// here, so this pins the shape instead: the build holds the very values the
// plan compared, not an equal copy read again.
func TestOfflineBuildInstallsTheKeysAndCertificatesItChecked(t *testing.T) {
	options, _ := newTestOptions(t)
	writeSampleKeys(t, options.RecipeDir)
	writeRecipeFile(t, options.RecipeDir, "certs/corp.pem", certificatePEM(t, "Corp Root CA"))
	imageRecipe := sampleRecipe()
	imageRecipe.Sources = sampleSources()
	imageRecipe.Packages.Include = []string{"git", "docker-ce"}
	imageRecipe.Certificates = &recipe.Certificates{Include: []string{"certs/corp.pem"}}
	bootstrapper := &fakeBootstrapper{dpkgStatus: sampleDpkgStatusWithSources, aptLists: sampleAptListsWithSources()}
	if _, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), imageRecipe, options); err != nil {
		t.Fatal(err)
	}

	options.Offline = true
	build, offline, err := newUbuntuBuild(&offlineFakeBootstrapper{}, imageRecipe, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(offline.sourceKeys) != len(imageRecipe.Sources) || len(offline.certificates.Files) != 1 {
		t.Fatalf("the plan kept %d keys and %d certificates, want %d and 1", len(offline.sourceKeys), len(offline.certificates.Files), len(imageRecipe.Sources))
	}
	if reflect.ValueOf(build.sourceKeys).UnsafePointer() != reflect.ValueOf(offline.sourceKeys).UnsafePointer() {
		t.Error("the build installs keys read again after the lock check")
	}
	if &build.certificates.Files[0].PEM[0] != &offline.certificates.Files[0].PEM[0] {
		t.Error("the build installs certificates read again after the lock check")
	}
}
