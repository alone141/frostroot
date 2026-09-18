package form

import (
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

func TestAnEditKeepsTheRecipesCertificates(t *testing.T) {
	// A round trip through the form with the certificates answer untouched
	// must leave them exactly as they were rather than drop them.
	original := recipe.Recipe{
		Image:        recipe.Image{Name: "lab", Release: "24.04", Arch: "amd64"},
		User:         recipe.User{Name: "student", Sudo: true},
		Packages:     recipe.Packages{Include: []string{"git"}},
		Certificates: &recipe.Certificates{Include: []string{"certs/corp-root.pem", "certs/other.pem"}},
	}

	roundTripped := ToRecipe(FromRecipe(original))
	if roundTripped.Certificates == nil {
		t.Fatal("an edit dropped the [certificates] table")
	}
	if strings.Join(roundTripped.CertificatePaths(), " ") != "certs/corp-root.pem certs/other.pem" {
		t.Errorf("an edit rewrote the certificates as %v", roundTripped.CertificatePaths())
	}
	if summary := Summary(FromRecipe(original)); !strings.Contains(summary, "certs/corp-root.pem") {
		t.Errorf("the summary does not show the certificates:\n%s", summary)
	}

	// A recipe that names none keeps no table, as with [python].
	original.Certificates = nil
	if plain := ToRecipe(FromRecipe(original)); plain.Certificates != nil {
		t.Errorf("a recipe with no certificates gained a table: %+v", plain.Certificates)
	}
}
