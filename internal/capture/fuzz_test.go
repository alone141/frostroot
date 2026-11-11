package capture

import (
	"bytes"
	"testing"
)

// FuzzMachineFiles: capture reads a machine it did not set up, whose dpkg
// files and apt sources may be anything.
func FuzzMachineFiles(f *testing.F) {
	f.Add([]byte("Package: curl\nStatus: install ok installed\nArchitecture: amd64\nVersion: 8.5.0-2ubuntu10\nDescription: tool\n more\n\n"))
	f.Add([]byte("deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu noble stable\n"))
	f.Add([]byte("Types: deb\nURIs: http://archive.ubuntu.com/ubuntu\nSuites: noble noble-updates\nComponents: main universe\nSigned-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = readStanzas(bytes.NewReader(data))
		_ = parseOneLineSources("/etc/apt/sources.list", string(data))
		_ = parseDeb822Sources("/etc/apt/sources.list.d/ubuntu.sources", string(data))
	})
}
