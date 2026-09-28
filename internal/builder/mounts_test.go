package builder

import "testing"

func TestIsMountRefusal(t *testing.T) {
	// Worded so in mmdebstrap 1.4.3 and 1.5.7. A real build as root without
	// CAP_SYS_ADMIN began with the second, third and fourth, and printed the
	// last two after its download.
	refusals := []string{
		"W: cannot execute mount",
		"W: cannot mount because CAP_SYS_ADMIN is not in the effective set",
		"W: cannot mount because CAP_SYS_ADMIN is not in the bounding set",
		"W: cannot mount because unshare --mount failed",
		"W: skipping mount sysfs",
		"W: skipping mount proc",
	}
	for _, line := range refusals {
		if !isMountRefusal(line) {
			t.Errorf("isMountRefusal(%q) = false, want true", line)
		}
	}
	// Lines an ordinary build prints, the warning among them.
	others := []string{
		"I: running apt-get update...",
		"W: Unable to read /var/tmp/frostroot-0/build-1/tmp/mmdebstrap.x/tmp/mmdebstrap.apt.conf.y - RealFileExists (2: No such file or directory)",
		"Setting up mount (2.39.3-9ubuntu6.6) ...",
		"",
	}
	for _, line := range others {
		if isMountRefusal(line) {
			t.Errorf("isMountRefusal(%q) = true, want false", line)
		}
	}
}

func TestMountWatchStopsOnceAtTheFirstRefusal(t *testing.T) {
	stops := 0
	watch := newMountWatch(func() { stops++ })
	// Lines split across writes, as a pipe delivers them.
	chunks := []string{
		"I: running apt-get upd",
		"ate...\nW: cannot mount be",
		"cause unshare --mount failed\r\nW: cannot execute mount\n",
		"W: skipping mount proc\n",
	}
	for _, chunk := range chunks {
		if written, err := watch.Write([]byte(chunk)); written != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v, want %d, nil", chunk, written, err, len(chunk))
		}
	}
	watch.flush()
	if want := "W: cannot mount because unshare --mount failed"; watch.refusal != want {
		t.Errorf("refusal = %q, want %q", watch.refusal, want)
	}
	if stops != 1 {
		t.Errorf("stop was called %d times, want once", stops)
	}
}

func TestMountWatchReadsALastLineWithoutNewline(t *testing.T) {
	stopped := false
	watch := newMountWatch(func() { stopped = true })
	_, _ = watch.Write([]byte("I: done\nW: cannot execute mount"))
	if stopped {
		t.Fatal("stopped on a line that was not complete yet")
	}
	watch.flush()
	if !stopped || watch.refusal != "W: cannot execute mount" {
		t.Errorf("after flush: stopped = %t, refusal = %q; want the last line seen", stopped, watch.refusal)
	}
}

func TestMountWatchLetsAnOrdinaryBuildGo(t *testing.T) {
	stopped := false
	watch := newMountWatch(func() { stopped = true })
	_, _ = watch.Write([]byte("I: running apt-get update...\nW: Unable to read /x/mmdebstrap.apt.conf.y - RealFileExists (2: No such file or directory)\nI: done\n"))
	watch.flush()
	if stopped || watch.refusal != "" {
		t.Errorf("stopped = %t, refusal = %q; want an ordinary build left alone", stopped, watch.refusal)
	}
}
