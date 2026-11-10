package builder

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"frostroot/internal/recipe"
)

func TestChooseFrozenInstant(t *testing.T) {
	now := time.Date(2026, time.September, 17, 12, 34, 56, 0, time.UTC)
	lockFrozenAt := &offlinePlan{lock: recipe.Lockfile{SourceDateEpoch: 1758067200}}
	oldLock := &offlinePlan{lock: recipe.Lockfile{FrostrootVersion: "0.5.0"}}
	testCases := []struct {
		name        string
		environment string // SOURCE_DATE_EPOCH; "" means unset
		offline     *offlinePlan
		want        frozenInstant
	}{
		{name: "online without the variable freezes at the build start", want: frozenInstant{epoch: now.Unix()}},
		{name: "online honors the variable", environment: "1758067200", want: frozenInstant{epoch: 1758067200}},
		{name: "offline freezes at the lock's instant", offline: lockFrozenAt, want: frozenInstant{epoch: 1758067200, fromLock: true}},
		{name: "offline ignores the variable", environment: "1700000000", offline: lockFrozenAt, want: frozenInstant{epoch: 1758067200, fromLock: true}},
		{name: "offline ignores even an unusable variable", environment: "yesterday", offline: lockFrozenAt, want: frozenInstant{epoch: 1758067200, fromLock: true}},
		{name: "an old lock gets the build start and no promise", offline: oldLock, want: frozenInstant{epoch: now.Unix()}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			getenv := fakeEnvironment(map[string]string{SourceDateEpochVariable: testCase.environment})
			instant, err := chooseFrozenInstant(getenv, testCase.offline, now)
			if err != nil {
				t.Fatal(err)
			}
			if instant != testCase.want {
				t.Errorf("instant = %+v, want %+v", instant, testCase.want)
			}
		})
	}
}

// TestChooseFrozenInstantRefusesAnInstantStillToCome: mmdebstrap dates no
// file later than the frozen instant and leaves an earlier date alone, so
// an instant still ahead of the clock leaves the build's own files dated by
// the clock, and two rebuilds differ while the summary says they cannot. A
// SOURCE_DATE_EPOCH in milliseconds, a common slip, is fifty thousand
// years ahead, and was recorded in the lock as it was.
func TestChooseFrozenInstantRefusesAnInstantStillToCome(t *testing.T) {
	now := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	ahead := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	milliseconds := strconv.FormatInt(now.Add(-time.Hour).UnixMilli(), 10)
	for name, test := range map[string]struct {
		environment string
		offline     *offlinePlan
		wantText    string
	}{
		"online, an hour ahead":       {environment: ahead, wantText: "later than this machine's clock"},
		"online, in milliseconds":     {environment: milliseconds, wantText: "milliseconds"},
		"offline, a lock a day ahead": {offline: &offlinePlan{lock: recipe.Lockfile{SourceDateEpoch: now.Add(24 * time.Hour).Unix()}}, wantText: "set the clock right"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := chooseFrozenInstant(fakeEnvironment(map[string]string{SourceDateEpochVariable: test.environment}), test.offline, now)
			if !errors.Is(err, ErrBadSourceDateEpoch) || !strings.Contains(err.Error(), test.wantText) {
				t.Errorf("error = %v, want ErrBadSourceDateEpoch saying %q", err, test.wantText)
			}
		})
	}
	// Clocks disagree by seconds: an instant a minute ahead is taken.
	aMinuteAhead := now.Add(time.Minute).Unix()
	if instant, err := chooseFrozenInstant(fakeEnvironment(map[string]string{SourceDateEpochVariable: strconv.FormatInt(aMinuteAhead, 10)}), nil, now); err != nil || instant.epoch != aMinuteAhead {
		t.Errorf("a minute ahead: instant %+v, error %v; want it taken", instant, err)
	}
}

func TestChooseFrozenInstantRefusesUnusableVariable(t *testing.T) {
	for _, value := range []string{"yesterday", "1758067200.5", "-1", "0", " 1758067200"} {
		t.Run(value, func(t *testing.T) {
			_, err := chooseFrozenInstant(fakeEnvironment(map[string]string{SourceDateEpochVariable: value}), nil, time.Now())
			if !errors.Is(err, ErrBadSourceDateEpoch) {
				t.Fatalf("error = %v, want ErrBadSourceDateEpoch", err)
			}
			for _, wantText := range []string{`"` + value + `"`, "offline build ignores it"} {
				if !strings.Contains(err.Error(), wantText) {
					t.Errorf("error = %v, want it to say %q", err, wantText)
				}
			}
		})
	}
}
