package builder

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frostroot/internal/recipe"
)

// certificatePEM returns one self-signed certificate as PEM.
func certificatePEM(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating a certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// writeRecipeFile writes content at a path relative to recipeDir.
func writeRecipeFile(t *testing.T, recipeDir, relativePath string, content []byte) {
	t.Helper()
	full := filepath.Join(recipeDir, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("making a directory: %v", err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatalf("writing %s: %v", relativePath, err)
	}
}

// certificateRecipe is a valid recipe naming certificatePaths.
func certificateRecipe(certificatePaths ...string) recipe.Recipe {
	imageRecipe := recipe.Recipe{
		Image: recipe.Image{Name: "lab", Release: "24.04", Arch: "amd64"},
		User:  recipe.User{Name: "student", Sudo: true},
	}
	if len(certificatePaths) > 0 {
		imageRecipe.Certificates = &recipe.Certificates{Include: certificatePaths}
	}
	return imageRecipe
}

func TestReadCertificatesSplitsABundleIntoOneFileEach(t *testing.T) {
	recipeDir := t.TempDir()
	first := certificatePEM(t, "Corp Root CA")
	second := certificatePEM(t, "Corp Issuing CA")
	bundle := append(append([]byte(nil), first...), second...)
	writeRecipeFile(t, recipeDir, "certs/corp.pem", bundle)

	set, err := ReadCertificates(recipeDir, []string{"certs/corp.pem"})
	if err != nil {
		t.Fatalf("ReadCertificates: %v", err)
	}
	if len(set.Files) != 2 {
		t.Fatalf("staged %d files, want 2", len(set.Files))
	}
	// update-ca-certificates hashes the first certificate of a file and
	// ignores the rest, so a bundle has to become two files.
	if set.Files[0].FileName != "corp.crt" || set.Files[1].FileName != "corp-2.crt" {
		t.Errorf("staged %q and %q, want corp.crt and corp-2.crt", set.Files[0].FileName, set.Files[1].FileName)
	}
	if string(set.PEM) != string(bundle) {
		t.Error("the concatenated trust is not both certificates")
	}
	if len(set.Locked) != 1 {
		t.Fatalf("locked %d entries, want one per file the recipe named", len(set.Locked))
	}
	digest := sha256.Sum256(bundle)
	if set.Locked[0].SHA256 != hex.EncodeToString(digest[:]) {
		t.Error("the lock records something other than the digest of the recipe's file")
	}
	if set.Locked[0].Name != "corp" || set.Locked[0].Path != "certs/corp.pem" {
		t.Errorf("locked %+v, want name corp at certs/corp.pem", set.Locked[0])
	}
}

func TestReadCertificatesRefusesWhatItCannotInstall(t *testing.T) {
	recipeDir := t.TempDir()
	writeRecipeFile(t, recipeDir, "certs/corp.pem", certificatePEM(t, "Corp Root CA"))
	writeRecipeFile(t, recipeDir, "other/corp.crt", certificatePEM(t, "Another Root CA"))
	writeRecipeFile(t, recipeDir, "certs/empty.pem", []byte("nothing here\n"))

	tests := []struct {
		name     string
		paths    []string
		wantText string
	}{
		{name: "two files with one name in the image", paths: []string{"certs/corp.pem", "other/corp.crt"}, wantText: "would both install as corp.crt"},
		{name: "a file that is not there", paths: []string{"certs/missing.pem"}, wantText: "certs/missing.pem"},
		{name: "a file that holds no certificate", paths: []string{"certs/empty.pem"}, wantText: "no certificate"},
		{name: "a path outside the recipe directory", paths: []string{"../corp.pem"}, wantText: "invalid certificate path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ReadCertificates(recipeDir, test.paths)
			if !errors.Is(err, ErrCertificate) {
				t.Fatalf("ReadCertificates returned %v, want ErrCertificate", err)
			}
			if !strings.Contains(err.Error(), test.wantText) {
				t.Errorf("the error does not say %q: %v", test.wantText, err)
			}
		})
	}
}

func TestCheckCertificatesAgainstLockReportsEveryDifference(t *testing.T) {
	corp := recipe.LockCertificate{Name: "corp", Path: "certs/corp.pem", SHA256: strings.Repeat("a", 64)}
	changed := recipe.LockCertificate{Name: "corp", Path: "certs/corp.pem", SHA256: strings.Repeat("b", 64)}
	other := recipe.LockCertificate{Name: "other", Path: "certs/other.pem", SHA256: strings.Repeat("c", 64)}

	tests := []struct {
		name     string
		read     []recipe.LockCertificate
		locked   []recipe.LockCertificate
		wantText string
	}{
		{name: "the same file", read: []recipe.LockCertificate{corp}, locked: []recipe.LockCertificate{corp}},
		{name: "nothing either way"},
		{name: "the file changed", read: []recipe.LockCertificate{changed}, locked: []recipe.LockCertificate{corp}, wantText: "changed since the lock was written"},
		{name: "a certificate the recipe added", read: []recipe.LockCertificate{corp, other}, locked: []recipe.LockCertificate{corp}, wantText: "certs/other.pem is not in the lock"},
		{name: "a certificate the recipe dropped", read: []recipe.LockCertificate{corp}, locked: []recipe.LockCertificate{corp, other}, wantText: "no longer names"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			problems := CheckCertificatesAgainstLock(test.read, test.locked)
			if test.wantText == "" {
				if len(problems) != 0 {
					t.Errorf("reported %v, want none", problems)
				}
				return
			}
			if len(problems) != 1 || !strings.Contains(problems[0], test.wantText) {
				t.Errorf("reported %v, want one mentioning %q", problems, test.wantText)
			}
		})
	}
}

func TestWriteAptCaInfoPutsTheHostsCertificatesFirst(t *testing.T) {
	workDir := t.TempDir()

	// Nothing to add: apt keeps its own store, and no file is written.
	path, err := writeAptCaInfo(workDir, nil)
	if err != nil || path != "" {
		t.Fatalf("writeAptCaInfo with no trust = %q, %v; want the host's own store", path, err)
	}

	extra := certificatePEM(t, "Corp Root CA")
	path, err = writeAptCaInfo(workDir, extra)
	if err != nil {
		t.Fatalf("writeAptCaInfo: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the bundle: %v", err)
	}
	if !strings.HasSuffix(string(written), string(extra)) {
		t.Error("the extra authority is not at the end of the bundle")
	}
	// CaInfo replaces apt's store rather than adding to it, so whatever the
	// host trusts has to be in the file as well.
	hostPEM, hostErr := os.ReadFile(HostTrustPath)
	if hostErr == nil && !strings.HasPrefix(string(written), string(hostPEM)) {
		t.Error("the host's own certificates are not at the front of the bundle")
	}
}

func TestRenderProvisionScriptInstallsTheRecipesCertificates(t *testing.T) {
	script, err := RenderProvisionScript(certificateRecipe("certs/corp.pem", "certs/other.pem"))
	if err != nil {
		t.Fatalf("RenderProvisionScript: %v", err)
	}
	if !strings.Contains(script, "update-ca-certificates") {
		t.Error("the script does not run update-ca-certificates")
	}
	for _, want := range []string{"'corp'", "'other'"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script does not check %s: %s", want, script)
		}
	}
	// The tool exits 0 having installed nothing when it cannot read a file,
	// so the script checks the result.
	if !strings.Contains(script, "/etc/ssl/certs/$certificate.pem") {
		t.Error("the script trusts update-ca-certificates' exit code instead of its result")
	}

	without, err := RenderProvisionScript(certificateRecipe())
	if err != nil {
		t.Fatalf("RenderProvisionScript: %v", err)
	}
	if strings.Contains(without, "update-ca-certificates") {
		t.Error("a recipe with no certificates still runs update-ca-certificates")
	}
}

func TestRenderPythonScriptTellsPipWhatToVerifyAgainst(t *testing.T) {
	// pip verifies against the certificates vendored inside it, so an image
	// that trusts the proxy is not enough: --cert is the only thing it
	// honors, PIP_CERT having gone out with the rest of the PIP_ environment.
	tests := []struct {
		name        string
		options     PythonOptions
		wantCert    string
		wantAbsent  []string
		wantPresent []string
	}{
		{
			name:       "the recipe's own authorities",
			options:    PythonOptions{TrustImageCertificates: true},
			wantCert:   ImageTrustPath,
			wantAbsent: []string{"cat ", PythonExtraTrustPath},
		},
		{
			name:        "authorities the image does not install",
			options:     PythonOptions{ExtraTrust: true},
			wantCert:    PythonCertPath,
			wantPresent: []string{"cat '" + ImageTrustPath + "' '" + PythonExtraTrustPath + "' > '" + PythonCertPath + "'", "rm -f '" + PythonCertPath + "' '" + PythonExtraTrustPath + "'"},
		},
		{
			name:       "both, which is one bundle",
			options:    PythonOptions{TrustImageCertificates: true, ExtraTrust: true},
			wantCert:   PythonCertPath,
			wantAbsent: []string{"--cert \"$pipCert\" --require-hashes --requirement '" + PythonRequirementsPath + "'"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script := renderPythonScript(t, pythonRecipe(), test.options)
			if !strings.Contains(script, "pipCert='"+test.wantCert+"'") {
				t.Errorf("the script does not point pip at %s:\n%s", test.wantCert, script)
			}
			if strings.Count(script, `--cert "$pipCert"`) != 2 {
				t.Errorf("want --cert on both the pinned pip and the install, got %d:\n%s", strings.Count(script, `--cert "$pipCert"`), script)
			}
			for _, absent := range test.wantAbsent {
				if strings.Contains(script, absent) {
					t.Errorf("the script should not contain %q:\n%s", absent, script)
				}
			}
			for _, present := range test.wantPresent {
				if !strings.Contains(script, present) {
					t.Errorf("the script does not contain %q:\n%s", present, script)
				}
			}
		})
	}
}

func TestRenderPythonScriptTakesNoCertificateOffline(t *testing.T) {
	// An offline step installs from a directory and reaches no network, so
	// there is nothing to verify and nothing to leave behind.
	options := PythonOptions{Offline: true, TrustImageCertificates: true, ExtraTrust: true, Pip: PinnedPip}
	script := renderPythonScript(t, pythonRecipe(), options)
	for _, absent := range []string{"--cert", "pipCert", PythonExtraTrustPath, PythonCertPath} {
		if strings.Contains(script, absent) {
			t.Errorf("an offline script mentions %q:\n%s", absent, script)
		}
	}
}

func TestWriteStageKeepsTheBuildsOwnTrustOutOfTheImage(t *testing.T) {
	// --ca-bundle is trusted while fetching and never installed, so it is
	// staged only when there is a Python step to hand it to.
	extra := certificatePEM(t, "Proxy Root CA")
	withPython := pythonRecipe()

	stage, err := WriteStage(filepath.Join(t.TempDir(), "stage"), withPython, StageOptions{ExtraTrust: extra})
	if err != nil {
		t.Fatalf("WriteStage: %v", err)
	}
	if stage.ExtraTrustPath == "" {
		t.Fatal("the build's own trust was not staged for the Python step")
	}
	hooks := strings.Join(CustomizeHooks(stage), "\n")
	if !strings.Contains(hooks, "upload '"+stage.ExtraTrustPath+"' "+PythonExtraTrustPath) {
		t.Errorf("no hook uploads the build's own trust:\n%s", hooks)
	}
	if strings.Contains(hooks, ImageCertificateDir) {
		t.Errorf("the build's own trust reached the image's certificate directory:\n%s", hooks)
	}

	withoutPython, err := WriteStage(filepath.Join(t.TempDir(), "stage"), certificateRecipe(), StageOptions{ExtraTrust: extra})
	if err != nil {
		t.Fatalf("WriteStage: %v", err)
	}
	if withoutPython.ExtraTrustPath != "" {
		t.Error("a recipe with no Python step staged the build's own trust anyway")
	}

	// Offline nothing fetches, so the step neither uses the file nor deletes
	// it: uploading it would leave it in the image, and --ca-bundle would
	// change the bytes of a rebuild.
	offline, err := WriteStage(filepath.Join(t.TempDir(), "stage"), withPython, StageOptions{
		ExtraTrust: extra,
		Python:     PythonOptions{Offline: true, Pip: PinnedPip},
		WheelsDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("WriteStage: %v", err)
	}
	if offline.ExtraTrustPath != "" {
		t.Error("an offline rebuild staged a file nothing would remove from the image")
	}
	if hooks := strings.Join(CustomizeHooks(offline), "\n"); strings.Contains(hooks, PythonExtraTrustPath) {
		t.Errorf("an offline rebuild uploads the build's own trust:\n%s", hooks)
	}
}

func TestWriteStageUploadsTheCertificatesBeforeProvisioning(t *testing.T) {
	stageDir := filepath.Join(t.TempDir(), "stage")
	imageRecipe := certificateRecipe("certs/corp.pem")
	corp := certificatePEM(t, "Corp Root CA")

	stage, err := WriteStage(stageDir, imageRecipe, StageOptions{
		Certificates: []StagedCertificate{{FileName: "corp.crt", PEM: corp}},
	})
	if err != nil {
		t.Fatalf("WriteStage: %v", err)
	}
	staged, err := os.ReadFile(filepath.Join(stage.CertificateDir, "corp.crt"))
	if err != nil {
		t.Fatalf("reading the staged certificate: %v", err)
	}
	if string(staged) != string(corp) {
		t.Error("the staged certificate is not what was given")
	}

	hooks := CustomizeHooks(stage)
	uploadedAt, provisionedAt := -1, -1
	for index, hook := range hooks {
		if strings.Contains(hook, ImageCertificateDir+"/corp.crt") {
			uploadedAt = index
		}
		if strings.Contains(hook, provisionScriptName) {
			provisionedAt = index
		}
	}
	if uploadedAt < 0 {
		t.Fatalf("no hook uploads the certificate: %v", hooks)
	}
	if provisionedAt < 0 {
		t.Fatalf("no hook runs the provision script: %v", hooks)
	}
	// The provision script is what runs update-ca-certificates, so the files
	// have to be there first.
	if uploadedAt > provisionedAt {
		t.Errorf("the certificate is uploaded after provisioning: %v", hooks)
	}
}
