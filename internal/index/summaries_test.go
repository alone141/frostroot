package index

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// summaryServer answers /pypi/<name>/json the way PyPI does.
type summaryServer struct {
	requests atomic.Int64
	byName   map[string]string
	status   map[string]int
	block    chan struct{}
}

func newSummaryServer(t *testing.T) (*Summaries, *summaryServer) {
	t.Helper()
	served := &summaryServer{
		byName: map[string]string{"requests": "Python HTTP for Humans.", "numpy": "Fundamental package for array computing"},
		status: map[string]int{},
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		served.requests.Add(1)
		name := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/pypi/"), "/json")
		if served.block != nil {
			<-served.block
		}
		if code, isSet := served.status[name]; isSet {
			http.Error(response, "no", code)
			return
		}
		summary, isThere := served.byName[name]
		if !isThere {
			http.NotFound(response, request)
			return
		}
		if summary == "malformed" {
			_, _ = response.Write([]byte("{not json"))
			return
		}
		_, _ = response.Write([]byte(`{"info":{"summary":` + quoteJSON(summary) + `}}`))
	}))
	t.Cleanup(server.Close)
	return NewSummaries(server.Client(), nil, server.URL), served
}

func quoteJSON(text string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(text, `\`, `\\`), `"`, `\"`) + `"`
}

func TestSummariesFetchAndCache(t *testing.T) {
	summaries, served := newSummaryServer(t)
	if _, known := summaries.Cached("requests"); known {
		t.Error("nothing is known before anything is asked")
	}
	summaries.Fetch(context.Background(), "requests")
	summary, known := summaries.Cached("requests")
	if !known || summary != "Python HTTP for Humans." {
		t.Errorf("Cached = %q, %v", summary, known)
	}
	// Asked about again, it is not fetched again.
	before := served.requests.Load()
	summaries.Fetch(context.Background(), "requests")
	if served.requests.Load() != before {
		t.Error("a known summary was fetched twice")
	}
	// And the name is compared as PEP 503 compares it.
	if summary, known := summaries.Cached("Requests"); !known || summary == "" {
		t.Errorf("Cached(Requests) = %q, %v; want the same project", summary, known)
	}
}

func TestSummariesRememberAFailureAsEmpty(t *testing.T) {
	summaries, served := newSummaryServer(t)
	served.status["numpy"] = http.StatusInternalServerError
	for _, name := range []string{"nothing-here", "numpy"} {
		t.Run(name, func(t *testing.T) {
			summaries.Fetch(context.Background(), name)
			summary, known := summaries.Cached(name)
			if !known || summary != "" {
				t.Errorf("Cached = %q, %v; want a known-empty summary", summary, known)
			}
			// A missing summary is a missing nicety, asked about once: on
			// every cursor move it would be a request a keystroke.
			before := served.requests.Load()
			summaries.Fetch(context.Background(), name)
			if served.requests.Load() != before {
				t.Error("a failure was fetched again")
			}
		})
	}
}

func TestSummariesLeaveACanceledLookupUnknown(t *testing.T) {
	summaries, served := newSummaryServer(t)
	served.block = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		summaries.Fetch(ctx, "requests")
	}()
	cancel()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("Fetch did not return when its context ended")
	}
	close(served.block)
	if _, known := summaries.Cached("requests"); known {
		// Remembering "" here would mean the row never gets its summary.
		t.Error("a canceled lookup must leave the name unknown, so resting on it again asks")
	}
}

func TestSummariesSurviveConcurrentReaders(t *testing.T) {
	summaries, _ := newSummaryServer(t)
	var waiting sync.WaitGroup
	for range 8 {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			summaries.Fetch(context.Background(), "requests")
			summaries.Cached("numpy")
		}()
	}
	waiting.Wait()
	if summary, known := summaries.Cached("requests"); !known || summary == "" {
		t.Errorf("Cached = %q, %v", summary, known)
	}
}

func TestCleanSummary(t *testing.T) {
	tests := map[string]string{
		"Python HTTP for Humans.":   "Python HTTP for Humans.",
		"  spaces   everywhere\t":   "spaces everywhere",
		"two\nlines":                "two lines",
		"a\x00control\x07character": "acontrolcharacter",
		"":                          "",
		strings.Repeat("x", 250):    strings.Repeat("x", 200) + "…",
	}
	for summary, want := range tests {
		if got := cleanSummary(summary); got != want {
			t.Errorf("cleanSummary(%q) = %q, want %q", summary, got, want)
		}
	}
}

func TestSummariesReadAMalformedBodyAsNothing(t *testing.T) {
	summaries, served := newSummaryServer(t)
	served.byName["broken"] = "malformed"
	summaries.Fetch(context.Background(), "broken")
	if summary, known := summaries.Cached("broken"); !known || summary != "" {
		t.Errorf("Cached = %q, %v; want a known-empty summary", summary, known)
	}
}

func TestSummaryBaseURL(t *testing.T) {
	// Whoever the index belongs to is who gets asked: pointing frostroot at
	// a private index must not have it telling pypi.org what was looked up.
	for indexURL, want := range map[string]string{
		"":                                "https://pypi.org/pypi",
		PyPIIndexURL:                      "https://pypi.org/pypi",
		"https://devpi.corp/root/simple/": "https://devpi.corp/root/pypi",
		"https://devpi.corp/root/simple":  "https://devpi.corp/root/pypi",
		"http://127.0.0.1:8099":           "http://127.0.0.1:8099/pypi",
	} {
		if got := SummaryBaseURL(indexURL); got != want {
			t.Errorf("SummaryBaseURL(%q) = %q, want %q", indexURL, got, want)
		}
	}
}
