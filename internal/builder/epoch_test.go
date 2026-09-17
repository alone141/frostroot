package builder

import (
	"errors"
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
