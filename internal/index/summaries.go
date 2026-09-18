package index

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"frostroot/internal/pki"
	"frostroot/internal/recipe"
)

// summaryTimeout bounds one lookup. A summary is a nicety: waiting for a
// slow one must not be how the picker feels.
const summaryTimeout = 10 * time.Second

// maxSummaryBytes bounds what one lookup reads. A project's JSON lists
// every file of every release, which for numpy is megabytes; the summary is
// in the first few hundred bytes of it, but the decoder needs the object, so
// this is a ceiling against a pathological one rather than a budget.
const maxSummaryBytes = 8 << 20

// summaryLimit is how many characters of a summary are kept. PyPI does not
// bound the field, and the picker shows one line.
const summaryLimit = 200

// Summaries fetches the one-line summary of a PyPI project and remembers
// it. PyPI publishes no summaries in bulk — one request a project, and
// there are 894,088 of them — so nothing is fetched until something asks
// for a particular name.
//
// It is safe for concurrent use: the widget reads what is cached while a
// command fetches.
type Summaries struct {
	client  *http.Client
	baseURL string

	mutex  sync.Mutex
	byName map[string]string
}

// NewSummaries returns a fetcher. client may be nil, and rootCAs carries
// --ca-bundle's pool, since pypi.org is HTTPS and a proxy that inspects TLS
// would otherwise break this where it cannot break the Ubuntu archive.
func NewSummaries(client *http.Client, rootCAs *x509.CertPool) *Summaries {
	if client == nil {
		client = &http.Client{Timeout: summaryTimeout, Transport: pki.Transport(rootCAs)}
	}
	return &Summaries{client: client, baseURL: "https://pypi.org/pypi", byName: map[string]string{}}
}

// Cached returns the summary of name if it has been fetched, and never
// blocks: it is what a View draws from. The second result distinguishes a
// project whose summary is known to be empty — PyPI allows that, and so
// does a lookup that failed — from one nobody has asked about.
func (s *Summaries) Cached(name string) (summary string, known bool) {
	normalized := recipe.NormalizePythonName(name)
	s.mutex.Lock()
	defer s.mutex.Unlock()
	summary, known = s.byName[normalized]
	return summary, known
}

// Fetch looks the summary up unless it is already known, and remembers it.
// A failure is remembered as an empty summary, so that a name is asked
// about once: a missing summary is a missing nicety, never an error the
// user is shown, and retrying on every cursor move would be a request a
// keystroke.
func (s *Summaries) Fetch(ctx context.Context, name string) {
	normalized := recipe.NormalizePythonName(name)
	if _, known := s.Cached(normalized); known {
		return
	}
	summary := s.lookUp(ctx, name)
	// A canceled lookup is not an answer: leave it unknown so that resting
	// on the row again asks properly.
	if ctx.Err() != nil {
		return
	}
	s.mutex.Lock()
	s.byName[normalized] = summary
	s.mutex.Unlock()
}

// lookUp asks PyPI for one project, returning "" for anything that does not
// end in a summary.
func (s *Summaries) lookUp(ctx context.Context, name string) string {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s/json", s.baseURL, name), nil)
	if err != nil {
		return ""
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return ""
	}
	defer func() { _ = response.Body.Close() }() // read, or the lookup failed
	if response.StatusCode != http.StatusOK {
		return ""
	}
	var document struct {
		Info struct {
			Summary string `json:"summary"`
		} `json:"info"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxSummaryBytes)).Decode(&document); err != nil {
		return ""
	}
	return cleanSummary(document.Info.Summary)
}

// cleanSummary makes a summary one short line: PyPI does not stop anyone
// writing a paragraph, a tab or a control character into it.
func cleanSummary(summary string) string {
	summary = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\t' {
			return ' '
		}
		if character < ' ' {
			return -1
		}
		return character
	}, summary)
	summary = strings.Join(strings.Fields(summary), " ")
	if runes := []rune(summary); len(runes) > summaryLimit {
		summary = strings.TrimRight(string(runes[:summaryLimit]), " ") + "…"
	}
	return summary
}
