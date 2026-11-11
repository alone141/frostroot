package builder

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// FuzzImageRecords: what a build reads back out of the image it made, dpkg's
// status, apt's marks and pip's reports, is written by programs inside it;
// none of it may panic the build that reads it.
func FuzzImageRecords(f *testing.F) {
	report, err := os.ReadFile(filepath.Join("testdata", "pip-report.json"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte(sampleDpkgStatus))
	f.Add([]byte(sampleExtendedStates))
	f.Add(report)
	f.Add([]byte(`[{"name":"numpy","version":"2.5.3"}]`))
	f.Add([]byte(`{"version":"1","install":[{"metadata":{"name":"pip","version":"24.3.1"}}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseDpkgStatus(bytes.NewReader(data))
		_, _ = ParseExtendedStates(bytes.NewReader(data))
		_, _ = ParsePipReport(bytes.NewReader(data), []string{"numpy", "requests"})
		_, _ = ParsePipList(bytes.NewReader(data))
		_, _ = ParsePinReport(bytes.NewReader(data))
	})
}
