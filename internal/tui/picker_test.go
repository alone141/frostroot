package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"frostroot/internal/form"
	"frostroot/internal/recipe"
)

// samplePackages are real entries of noble's archive, enough of them for a
// search to have something to rank.
var samplePackages = []form.Match{
	{Name: "catch2", Version: "3.4.0-1build1", Component: "universe", Section: "devel", Description: "C++ Automated Test Cases in Headers"},
	{Name: "cmake", Version: "3.28.3-1build7", Component: "main", Section: "devel", Description: "cross-platform, open-source make system"},
	{Name: "cmake-data", Version: "3.28.3-1build7", Component: "main", Section: "devel", Description: "CMake data files (modules, templates and documentation)"},
	{Name: "cmake-extras", Version: "1.7-2", Component: "universe", Section: "libs", Description: "Extra CMake utility modules"},
	{Name: "cmake-format", Version: "0.6.13-5", Component: "universe", Section: "devel", Description: "source code formatter for cmake listfiles"},
	{Name: "extra-cmake-modules", Version: "5.115.0-0ubuntu5", Component: "universe", Section: "libs", Description: "Extra modules and scripts for CMake"},
	{Name: "git", Version: "1:2.43.0-1ubuntu7.3", Component: "main", Section: "vcs", Description: "fast, scalable, distributed revision control system"},
	{Name: "jq", Version: "1.7.1-3build1", Component: "main", Section: "utils", Description: "lightweight and flexible command-line JSON processor"},
	{Name: "meson", Version: "1.3.2-1ubuntu1", Component: "universe", Section: "devel", Description: "high-productivity build system"},
	{Name: "ninja-build", Version: "1.11.1-2", Component: "universe", Section: "devel", Description: "small build system closest in spirit to Make"},
	{Name: "unrar", Version: "1:7.0.7-1build1", Component: "multiverse", Section: "utils", Description: "Unarchiver for .rar files (non-free version)"},
	{Name: "valgrind", Version: "1:3.22.0-0ubuntu3", Component: "main", Section: "devel", Description: "instrumentation framework for building dynamic analysis tools"},
	// A package no Ubuntu archive has: it comes from the source the recipe
	// added, and the row says so.
	{Name: "docker-ce", Version: "5:27.3.1-1~ubuntu.24.04~noble", Component: "stable", Section: "admin", Description: "Docker: the open-source application container engine", Origin: "docker"},
}

// sampleIndex is a form.PackageIndex over samplePackages that ranks the way
// internal/index does: the exact name, names starting with the query, names
// holding it, then descriptions.
type sampleIndex struct{}

func (sampleIndex) rank(match form.Match, query string) int {
	name, description := strings.ToLower(match.Name), strings.ToLower(match.Description)
	switch {
	case query == "" || strings.Contains(name, query) && name != query && !strings.HasPrefix(name, query):
		return 2
	case name == query:
		return 0
	case strings.HasPrefix(name, query):
		return 1
	case strings.Contains(description, query):
		return 3
	}
	return -1
}

func (s sampleIndex) Search(query, section string, limit int) ([]form.Match, int) {
	query = strings.ToLower(strings.TrimSpace(query))
	var matches []form.Match
	for _, match := range samplePackages {
		if (section == "" || match.Section == section) && s.rank(match, query) >= 0 {
			matches = append(matches, match)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		left, right := s.rank(matches[i], query), s.rank(matches[j], query)
		if left != right {
			return left < right
		}
		return len(matches[i].Name) < len(matches[j].Name)
	})
	total := len(matches)
	return matches[:min(limit, total)], total
}

func (s sampleIndex) SectionsMatching(query string) []form.SectionCount {
	matches, _ := s.Search(query, "", len(samplePackages))
	counts := map[string]int{}
	for _, match := range matches {
		counts[match.Section]++
	}
	var sections []form.SectionCount
	for name, count := range counts {
		sections = append(sections, form.SectionCount{Name: name, Count: count})
	}
	sort.Slice(sections, func(i, j int) bool {
		if sections[i].Count != sections[j].Count {
			return sections[i].Count > sections[j].Count
		}
		return sections[i].Name < sections[j].Name
	})
	return sections
}

func (sampleIndex) Lookup(name string) (form.Match, bool) {
	for _, match := range samplePackages {
		if match.Name == name {
			return match, true
		}
	}
	return form.Match{}, false
}

func (sampleIndex) Has(name string) bool {
	return slices.ContainsFunc(samplePackages, func(match form.Match) bool { return match.Name == name })
}

func (sampleIndex) Nearest(name string, _ int) []string {
	if name == "ninja-buld" {
		return []string{"ninja-build"}
	}
	return nil
}

func (sampleIndex) Describe() string { return "noble · 85,574 packages · fetched 2 days ago" }

// openSampleIndex is a form.IndexOpener that has the index at once.
func openSampleIndex(context.Context, form.IndexRequest, func(int64, int64)) (form.PackageIndex, error) {
	return sampleIndex{}, nil
}

// sampleUnionIndex is the same index as the picker sees it once a source has
// been chosen on the page before: the same packages, and a line naming both
// repositories they were searched in.
type sampleUnionIndex struct{ sampleIndex }

func (sampleUnionIndex) Describe() string {
	return "noble + docker · 85,726 packages · fetched 2 days ago"
}

// openSampleUnionIndex is a form.IndexOpener for a recipe that adds a source.
func openSampleUnionIndex(context.Context, form.IndexRequest, func(int64, int64)) (form.PackageIndex, error) {
	return sampleUnionIndex{}, nil
}

// pickerDriver drives a pickerField on its own, outside a form.
type pickerDriver struct {
	*driver
	picker *pickerField
	answer *string
}

// newPickerDriver focuses a picker whose answer starts as initial. The
// catalog's answer is catalogChosen, and the release 24.04.
func newPickerDriver(t *testing.T, opener form.IndexOpener, initial string, catalogChosen ...string) *pickerDriver {
	t.Helper()
	answer := initial
	field := form.Field{Key: form.KeyOtherPackages, Title: "Other packages", Description: "any package of the release", OpenIndex: opener, Validate: func(text string) error {
		if strings.Contains(text, "_") {
			return errors.New("invalid package name")
		}
		return nil
	}}
	picker := newPickerField(context.Background(), field, &answer, func() form.IndexRequest { return form.IndexRequest{Release: "24.04"} }, func() []string { return catalogChosen }, unicodeGlyphs)
	picker.WithWidth(80)
	d := &pickerDriver{driver: newDriver(t, picker), picker: picker, answer: &answer}
	d.settle(picker.Focus())
	return d
}

func (d *pickerDriver) typeText(text string) {
	for _, character := range text {
		d.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{character}})
	}
}

var (
	pressSpace = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	pressDown  = tea.KeyMsg{Type: tea.KeyDown}
	pressSlash = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
)

// rowNames returns the names the list holds, the as-typed row in quotes.
func (d *pickerDriver) rowNames() []string {
	var rowNames []string
	for _, row := range d.picker.rows {
		if row.asTyped {
			rowNames = append(rowNames, fmt.Sprintf("%q", row.name))
			continue
		}
		rowNames = append(rowNames, row.name)
	}
	return rowNames
}

// movesOn reports whether pressing msg makes the field ask the form for
// the next one.
func (d *pickerDriver) movesOn(msg tea.Msg) bool {
	_, cmd := d.picker.Update(msg)
	return cmd != nil && fmt.Sprintf("%T", cmd()) == "huh.nextFieldMsg"
}

func TestPickerSearchesAndAddsTheExactName(t *testing.T) {
	d := newPickerDriver(t, openSampleIndex, "")
	d.typeText("cmake")
	want := []string{"cmake", "cmake-data", "cmake-extras", "cmake-format", "extra-cmake-modules"}
	if got := d.rowNames(); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	d.press(pressSpace)
	if *d.answer != "cmake" {
		t.Errorf("answer = %q, want cmake", *d.answer)
	}
	// A name typed in full and added is done with: the query goes, and the
	// list shows what is chosen.
	if d.picker.input.Value() != "" || !slices.Equal(d.rowNames(), []string{"cmake"}) {
		t.Errorf("query %q, rows %v; want an empty query and the chosen name", d.picker.input.Value(), d.rowNames())
	}
	view := d.picker.View()
	for _, wantText := range []string{"Other packages", "chosen (1): cmake", "3.28.3-1build7", "cross-platform", "all sections · noble · 85,574 packages"} {
		if !strings.Contains(view, wantText) {
			t.Errorf("view lacks %q:\n%s", wantText, view)
		}
	}
}

func TestPickerOffersWhatWasTypedBeforeItsNeighbors(t *testing.T) {
	d := newPickerDriver(t, openSampleIndex, "")
	d.typeText("cmak")
	if got := d.rowNames(); len(got) < 2 || got[0] != `"cmak"` || got[1] != "cmake" {
		t.Fatalf("rows = %v, want what was typed first, then the matches", got)
	}
	d.press(pressDown)
	d.press(pressSpace)
	d.press(pressDown)
	d.press(pressSpace)
	if *d.answer != "cmake cmake-data" {
		t.Errorf("answer = %q, want the two picked neighbors", *d.answer)
	}
	if d.picker.input.Value() != "cmak" {
		t.Errorf("query = %q; a search that was picked from stays, its neighbors may be wanted too", d.picker.input.Value())
	}
	if view := d.picker.View(); !strings.Contains(view, "5 found") {
		t.Errorf("view should count the matches:\n%s", view)
	}
}

func TestPickerAddsANameTheArchiveLacksAndSaysSo(t *testing.T) {
	d := newPickerDriver(t, openSampleIndex, "")
	d.typeText("ninja-buld")
	if got := d.rowNames(); !slices.Equal(got, []string{`"ninja-buld"`}) {
		t.Fatalf("rows = %v, want only what was typed", got)
	}
	if view := d.picker.View(); !strings.Contains(view, `add "ninja-buld" as typed · not in the index`) {
		t.Errorf("view should offer the name as typed:\n%s", view)
	}
	d.press(pressSpace)
	if *d.answer != "ninja-buld" {
		t.Errorf("answer = %q: a warning, never a refusal", *d.answer)
	}
	view := d.picker.View()
	for _, wantText := range []string{"nearest: ninja-build", "? not in the index", "chosen (1): ninja-buld"} {
		if !strings.Contains(view, wantText) {
			t.Errorf("view lacks %q:\n%s", wantText, view)
		}
	}
	// Space on the chosen row takes it out again.
	d.press(pressSpace)
	if *d.answer != "" {
		t.Errorf("answer = %q after removing the only name", *d.answer)
	}
}

func TestPickerLeavesCatalogNamesToTheCatalog(t *testing.T) {
	d := newPickerDriver(t, openSampleIndex, "", "git")
	d.typeText("git")
	d.press(pressSpace)
	if *d.answer != "" {
		t.Errorf("answer = %q; git is the catalog's", *d.answer)
	}
	view := d.picker.View()
	if !strings.Contains(view, "git is chosen in the catalog above") {
		t.Errorf("view should say where git is chosen:\n%s", view)
	}
	// The row carries the theme's mark for a chosen option, though the
	// picker's own answer does not hold the name.
	if frame := frameOf(view); !strings.Contains(frame, "[•] git") {
		t.Errorf("git should be marked as chosen:\n%s", frame)
	}
}

func TestPickerHoldsEnterBackOnceForANameNotAdded(t *testing.T) {
	d := newPickerDriver(t, openSampleIndex, "")
	d.typeText("valgrind")
	if d.movesOn(pressEnter) {
		t.Fatal("Enter moved on and dropped what was typed, without a word")
	}
	if view := d.picker.View(); !strings.Contains(view, `"valgrind" is not added`) {
		t.Errorf("view should say the name is not added:\n%s", view)
	}
	if !d.movesOn(pressEnter) {
		t.Error("the second Enter means it")
	}

	// With the name added, or nothing typed, Enter moves on at once.
	added := newPickerDriver(t, openSampleIndex, "")
	added.typeText("valgrind")
	added.press(pressSpace)
	if !added.movesOn(pressEnter) {
		t.Error("Enter after adding should move on")
	}
}

func TestPickerTakesAListTypedOrPasted(t *testing.T) {
	d := newPickerDriver(t, openSampleIndex, "", "git")
	d.typeText("jq,")
	if *d.answer != "jq" || d.picker.input.Value() != "" {
		t.Errorf("answer %q, query %q; a comma ends a name", *d.answer, d.picker.input.Value())
	}
	d.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("meson git, docker-ce\nBad_Name jq"), Paste: true})
	if *d.answer != "jq meson docker-ce" {
		t.Errorf("answer = %q, want the pasted names once each, the catalog's and the invalid left out", *d.answer)
	}
	if view := d.picker.View(); !strings.Contains(view, "not package names, left out: Bad_Name") {
		t.Errorf("view should say what was left out:\n%s", view)
	}
}

func TestPickerNarrowsBySection(t *testing.T) {
	d := newPickerDriver(t, openSampleIndex, "")
	d.typeText("cmake")
	d.press(pressSlash)
	view := d.picker.View()
	for _, wantText := range []string{"all sections", "devel", "libs", "enter picks the section"} {
		if !strings.Contains(view, wantText) {
			t.Errorf("the section list lacks %q:\n%s", wantText, view)
		}
	}
	// Enter picks a section here; it must not leave the field.
	d.press(pressDown) // devel 3
	d.press(pressDown) // libs 2
	if d.movesOn(pressEnter) {
		t.Fatal("Enter in the section list left the field")
	}
	if d.picker.section != "libs" || !slices.Equal(d.rowNames(), []string{"cmake", "cmake-extras", "extra-cmake-modules"}) {
		t.Errorf("section %q, rows %v", d.picker.section, d.rowNames())
	}
	if view := d.picker.View(); !strings.Contains(view, "section libs") {
		t.Errorf("the status should name the section:\n%s", view)
	}
	// Esc clears the query first, then the section.
	d.press(pressEsc)
	if d.picker.input.Value() != "" || d.picker.section != "libs" {
		t.Errorf("after one Esc: query %q, section %q", d.picker.input.Value(), d.picker.section)
	}
	if got := d.rowNames(); !slices.Equal(got, []string{"cmake-extras", "extra-cmake-modules"}) {
		t.Errorf("an empty query in a section browses it, got %v", got)
	}
	d.press(pressEsc)
	if d.picker.section != "" {
		t.Errorf("after two Esc: section %q", d.picker.section)
	}
}

func TestPickerWorksWhileTheIndexLoadsAndAfter(t *testing.T) {
	release := make(chan struct{})
	reportProgress := make(chan func(int64, int64), 1)
	opener := func(_ context.Context, _ form.IndexRequest, progress func(int64, int64)) (form.PackageIndex, error) {
		reportProgress <- progress
		<-release
		return sampleIndex{}, nil
	}
	answer := ""
	picker := newPickerField(context.Background(), form.Field{Key: form.KeyOtherPackages, Title: "Other packages", OpenIndex: opener}, &answer, func() form.IndexRequest { return form.IndexRequest{Release: "24.04"} }, func() []string { return nil }, unicodeGlyphs)
	picker.WithWidth(80)
	messages := make(chan tea.Msg, 16)
	runCommands(picker.Focus(), messages)
	progress := <-reportProgress
	if view := picker.View(); !strings.Contains(view, "opening the package index") {
		t.Errorf("view should say the index is on its way:\n%s", view)
	}
	progress(12_300_000, 19_400_000)
	if view := picker.View(); !strings.Contains(view, "12.3 MB / 19.4 MB") || !strings.Contains(view, "█") {
		t.Errorf("view should show how far the download is:\n%s", view)
	}

	// Meanwhile the field is a list editor.
	for _, character := range "htop" {
		picker.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{character}})
	}
	picker.Update(pressSpace)
	if answer != "htop" {
		t.Errorf("answer = %q; a name can be added before the index is there", answer)
	}

	close(release)
	picker.Update(awaitLoaded(t, messages))
	for _, character := range "meson" {
		picker.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{character}})
	}
	if view := picker.View(); !strings.Contains(view, "high-productivity build system") {
		t.Errorf("once loaded, the index answers:\n%s", view)
	}
}

// runCommands runs cmd, and the commands of a batch it turns out to be,
// each in its own goroutine, and sends what they return to messages.
func runCommands(cmd tea.Cmd, messages chan<- tea.Msg) {
	if cmd == nil {
		return
	}
	go func() {
		switch msg := cmd().(type) {
		case tea.BatchMsg:
			for _, batched := range msg {
				runCommands(batched, messages)
			}
		case nil:
		default:
			messages <- msg
		}
	}()
}

// awaitLoaded returns the message that says the index load ended.
func awaitLoaded(t *testing.T, messages <-chan tea.Msg) tea.Msg {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case msg := <-messages:
			if _, isLoaded := msg.(pickerLoadedMsg); isLoaded {
				return msg
			}
		case <-timeout:
			t.Fatal("the index load never ended")
		}
	}
}

func TestPickerWithoutAnIndexIsAListEditor(t *testing.T) {
	failing := func(context.Context, form.IndexRequest, func(int64, int64)) (form.PackageIndex, error) {
		return nil, errors.New("package index not available: dial tcp: no route to host")
	}
	for name, opener := range map[string]form.IndexOpener{"none given": nil, "cannot be had": failing} {
		t.Run(name, func(t *testing.T) {
			d := newPickerDriver(t, opener, "tree")
			d.typeText("htop")
			if got := d.rowNames(); !slices.Equal(got, []string{`"htop"`}) {
				t.Fatalf("rows = %v", got)
			}
			d.press(pressSpace)
			if *d.answer != "tree htop" {
				t.Errorf("answer = %q", *d.answer)
			}
			view := d.picker.View()
			if !strings.Contains(view, "names are added as typed and checked by build") {
				t.Errorf("view should say why nothing is searched:\n%s", view)
			}
			if strings.Contains(view, "not in the index") {
				t.Errorf("without an index nothing is known to be missing:\n%s", view)
			}
			d.press(pressSlash)
			if d.picker.choosingSection {
				t.Error("there are no sections without an index")
			}
		})
	}
}

func TestPickerOpensTheIndexTheAnswersAskFor(t *testing.T) {
	var opened []string
	opener := func(_ context.Context, request form.IndexRequest, _ func(int64, int64)) (form.PackageIndex, error) {
		names := []string{request.Release}
		for _, source := range request.Sources {
			names = append(names, source.Name)
		}
		opened = append(opened, strings.Join(names, "+"))
		return sampleIndex{}, nil
	}
	request := form.IndexRequest{Release: "24.04"}
	answer := ""
	picker := newPickerField(context.Background(), form.Field{Key: form.KeyOtherPackages, OpenIndex: opener}, &answer, func() form.IndexRequest { return request }, func() []string { return nil }, unicodeGlyphs)
	d := newDriver(t, picker)
	d.settle(picker.Focus())
	picker.Blur()
	d.settle(picker.Focus()) // the same answers: nothing to open again
	request.Release = "22.04"
	picker.Blur()
	d.settle(picker.Focus())
	// A source added on the page before this one is a repository the index
	// does not yet cover, so it is opened again.
	request.Sources = []recipe.Source{{Name: "docker", URL: "https://download.docker.com/linux/ubuntu"}}
	picker.Blur()
	d.settle(picker.Focus())
	if !slices.Equal(opened, []string{"24.04", "22.04", "22.04+docker"}) {
		t.Errorf("opened %v; want once, again when the release changes, and again when a source is added", opened)
	}
}

func TestPickerValidatesOnLeaving(t *testing.T) {
	d := newPickerDriver(t, openSampleIndex, "Bad_Name")
	if d.movesOn(pressEnter) {
		t.Fatal("an invalid answer moved on")
	}
	if d.picker.Error() == nil || !strings.Contains(d.picker.View(), "invalid package name") {
		t.Errorf("the error should be shown:\n%s", d.picker.View())
	}
	// Removing the name clears it.
	d.press(pressSpace)
	if !d.movesOn(pressEnter) {
		t.Errorf("answer %q should be acceptable", *d.answer)
	}
}

func TestPickerAnswersATickOnce(t *testing.T) {
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	opener := func(context.Context, form.IndexRequest, func(int64, int64)) (form.PackageIndex, error) {
		<-blocked
		return nil, errors.New("never")
	}
	answer := ""
	picker := newPickerField(context.Background(), form.Field{Key: form.KeyOtherPackages, OpenIndex: opener}, &answer, func() form.IndexRequest { return form.IndexRequest{Release: "24.04"} }, func() []string { return nil }, unicodeGlyphs)
	picker.Focus()
	tick := pickerTickMsg{tick: picker.tick}
	// A huh group hands its focused field every message twice.
	_, first := picker.Update(tick)
	_, second := picker.Update(tick)
	if first == nil || second != nil {
		t.Errorf("a tick delivered twice was answered %v and %v; want one new tick, or they double each round", first != nil, second != nil)
	}
}

func TestPickerKeepsItsHeight(t *testing.T) {
	d := newPickerDriver(t, openSampleIndex, "")
	empty := strings.Count(d.picker.View(), "\n")
	d.typeText("c")
	full := strings.Count(d.picker.View(), "\n")
	d.typeText("zzzz")
	none := strings.Count(d.picker.View(), "\n")
	if empty != full || full != none {
		t.Errorf("the field is %d, %d and %d lines high; the page must not jump as matches come and go", empty, full, none)
	}
}

func TestFormCarriesThePickersAnswer(t *testing.T) {
	host := noHost
	host.OpenIndex = openSampleIndex
	model := newFormModel(context.Background(), form.Fields(host), form.Defaults(noHost), nil)
	d := &formDriver{driver: newDriver(t, model), model: model}
	d.press(tea.WindowSizeMsg{Width: 100, Height: 40})
	for presses := 0; presses < maxKeyPresses && d.model.pages.GetFocusedField().GetKey() != form.KeyOtherPackages; presses++ {
		d.press(pressEnter)
	}
	if got := d.model.pages.GetFocusedField().GetKey(); got != form.KeyOtherPackages {
		t.Fatalf("never reached the picker; at %q", got)
	}
	for _, character := range "valgrind" {
		d.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{character}})
	}
	if view := d.model.View(); !strings.Contains(view, "instrumentation framework") || !strings.Contains(view, "space add/remove") {
		t.Errorf("the page should show the match and the picker's keys:\n%s", view)
	}
	d.press(pressSpace)
	d.pressEnterUntil(stageSummary)
	if got := d.model.binding.values().String(form.KeyOtherPackages); got != "valgrind" {
		t.Errorf("other_packages = %q, want valgrind", got)
	}
}

// pickerFormDriver drives the whole form to the picker: the page it lives
// on is part of how it looks.
func pickerFormDriver(t *testing.T, opener form.IndexOpener, otherPackages string, cols, rows int) *formDriver {
	t.Helper()
	host := noHost
	host.OpenIndex = opener
	initial := form.Defaults(noHost)
	initial[form.KeyOtherPackages] = otherPackages
	model := newFormModel(context.Background(), form.Fields(host), initial, nil)
	d := &formDriver{driver: newDriver(t, model), model: model}
	d.press(tea.WindowSizeMsg{Width: cols, Height: rows})
	for presses := 0; presses < maxKeyPresses && d.model.pages.GetFocusedField().GetKey() != form.KeyOtherPackages; presses++ {
		d.press(pressEnter)
	}
	if got := d.model.pages.GetFocusedField().GetKey(); got != form.KeyOtherPackages {
		t.Fatalf("never reached the picker; at %q", got)
	}
	return d
}

func (d *formDriver) typeText(text string) {
	for _, character := range text {
		d.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{character}})
	}
}

// pickerScenarios are the states of the picker the frames pin.
var pickerScenarios = []struct {
	name     string
	opener   form.IndexOpener
	packages string
	act      func(d *formDriver)
	allSizes bool
}{
	{name: "picker-search", opener: openSampleIndex, act: func(d *formDriver) { d.typeText("cmake") }, allSizes: true},
	{name: "picker-typed", opener: openSampleIndex, act: func(d *formDriver) { d.typeText("cmak"); d.press(pressDown) }},
	{name: "picker-sections", opener: openSampleIndex, act: func(d *formDriver) { d.typeText("cmake"); d.press(pressSlash); d.press(pressDown) }},
	{name: "picker-chosen", opener: openSampleIndex, packages: "valgrind ninja-buld meson", act: func(*formDriver) {}, allSizes: true},
	{name: "picker-held-back", opener: openSampleIndex, act: func(d *formDriver) { d.typeText("valgrind"); d.press(pressEnter) }},
	// What a source adds: the row names the repository it comes from, and
	// the line above the results names every repository searched.
	{name: "picker-source", opener: openSampleUnionIndex, act: func(d *formDriver) { d.typeText("docker") }, allSizes: true},
	{
		name: "picker-unavailable", packages: "tree",
		opener: func(context.Context, form.IndexRequest, func(int64, int64)) (form.PackageIndex, error) {
			return nil, errors.New("package index not available")
		},
		act: func(d *formDriver) { d.typeText("htop") },
	},
}

func TestPickerFrames(t *testing.T) {
	for _, scenario := range pickerScenarios {
		for _, size := range frameSizes {
			if !scenario.allSizes && size.cols != 80 {
				continue
			}
			t.Run(frameName(scenario.name, size.cols, size.rows), func(t *testing.T) {
				d := pickerFormDriver(t, scenario.opener, scenario.packages, size.cols, size.rows)
				scenario.act(d)
				assertFrame(t, frameName(scenario.name, size.cols, size.rows), d.model.View())
			})
		}
	}
	t.Run(frameName("picker-search-ascii", 80, 24), func(t *testing.T) {
		withLocale(t, map[string]string{"LANG": "C"})
		d := pickerFormDriver(t, openSampleIndex, "", 80, 24)
		d.typeText("cmake")
		assertFrame(t, frameName("picker-search-ascii", 80, 24), d.model.View())
	})
}

func TestPickerKnowsAPackageTheSectionFilterHides(t *testing.T) {
	d := newPickerDriver(t, openSampleIndex, "")
	d.typeText("cmake")
	d.press(pressSlash)
	d.press(pressDown) // devel
	d.press(pressDown) // libs
	d.press(pressEnter)
	// cmake is in devel; narrowed to libs the search does not return it,
	// and it is still a package of the archive, not something "as typed".
	if got := d.rowNames(); !slices.Equal(got, []string{"cmake", "cmake-extras", "extra-cmake-modules"}) {
		t.Fatalf("rows = %v, want cmake itself first, as the package it is", got)
	}
	if view := d.picker.View(); strings.Contains(view, "not in the index") || !strings.Contains(view, "cross-platform") {
		t.Errorf("cmake should be shown with its description:\n%s", view)
	}
	d.press(pressSpace)
	if *d.answer != "cmake" {
		t.Errorf("answer = %q", *d.answer)
	}
}

// summarySource is a form.PackageSummaries whose lookups the test releases.
type summarySource struct {
	sampleIndex
	mutex    sync.Mutex
	known    map[string]string
	requests []string
	release  chan struct{} // nil: answer at once
}

func newSummarySource() *summarySource {
	return &summarySource{known: map[string]string{}}
}

func (s *summarySource) Summary(name string) (string, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	summary, known := s.known[name]
	return summary, known
}

func (s *summarySource) FetchSummary(ctx context.Context, name string) {
	s.mutex.Lock()
	s.requests = append(s.requests, name)
	release := s.release
	s.mutex.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.known[name] = "what " + name + " is"
}

func (s *summarySource) asked() []string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return slices.Clone(s.requests)
}

// newSummaryDriver drives a picker whose index looks summaries up.
func newSummaryDriver(t *testing.T, source *summarySource) *pickerDriver {
	t.Helper()
	// The debounce that ships is a quarter of a second, which the driver
	// drops as a timer; shortened, the real scheduling runs in the test.
	previous := pickerSummaryDelay
	pickerSummaryDelay = time.Millisecond
	t.Cleanup(func() { pickerSummaryDelay = previous })
	answer := ""
	field := form.Field{
		Key: form.KeyPythonPackages, Title: "Python packages", Description: "type to search PyPI",
		OpenIndex: func(context.Context, form.IndexRequest, func(int64, int64)) (form.PackageIndex, error) {
			return source, nil
		},
		Summaries: true,
		Validate:  func(string) error { return nil },
	}
	picker := newPickerField(context.Background(), field, &answer, func() form.IndexRequest { return form.IndexRequest{Release: "24.04"} }, func() []string { return nil }, unicodeGlyphs)
	picker.WithWidth(80)
	d := &pickerDriver{driver: newDriver(t, picker), picker: picker, answer: &answer}
	d.settle(picker.Focus())
	return d
}

func TestPickerShowsASummaryForTheRowTheCursorRestsOn(t *testing.T) {
	source := newSummarySource()
	d := newSummaryDriver(t, source)
	d.typeText("cmake")
	// The debounce has elapsed inside the driver's settle, so the lookup for
	// the highlighted row has run.
	if asked := source.asked(); !slices.Contains(asked, "cmake") {
		t.Fatalf("asked %v, want the highlighted row", asked)
	}
	if view := d.picker.View(); !strings.Contains(view, "what cmake is") {
		t.Errorf("the summary belongs under the list:\n%s", view)
	}
	// Moving down asks about the new row, not the old one again.
	before := len(source.asked())
	d.press(pressDown)
	asked := source.asked()
	if len(asked) != before+1 || asked[len(asked)-1] != "cmake-data" {
		t.Errorf("asked %v after moving down", asked)
	}
	if view := d.picker.View(); !strings.Contains(view, "what cmake-data is") {
		t.Errorf("the summary should follow the cursor:\n%s", view)
	}
	// Coming back is answered from what is remembered, with no new lookup.
	before = len(source.asked())
	d.press(pressUp)
	if got := len(source.asked()); got != before {
		t.Errorf("a remembered summary was looked up again: %v", source.asked())
	}
}

func TestPickerIgnoresASupersededRest(t *testing.T) {
	source := newSummarySource()
	d := newSummaryDriver(t, source)
	d.typeText("cmake")
	if asked := source.asked(); !slices.Contains(asked, "cmake") {
		t.Fatalf("asked %v, want the highlighted row", asked)
	}
	// Holding Down schedules a rest a row; every one but the last is
	// superseded, and a superseded rest must not become a request.
	d.press(pickerSummaryDueMsg{tick: d.picker.summaryTick - 1, name: "cmake-data"})
	if slices.Contains(source.asked(), "cmake-data") {
		t.Error("a superseded rest was asked about")
	}
	// Nor may a rest on a row the cursor has since left.
	d.press(pickerSummaryDueMsg{tick: d.picker.summaryTick, name: "cmake-extras"})
	if slices.Contains(source.asked(), "cmake-extras") {
		t.Error("a rest on another row was asked about")
	}
}

func TestPickerSaysNothingWhenALookupGivesNothing(t *testing.T) {
	source := newSummarySource()
	d := newSummaryDriver(t, source)
	d.typeText("cmake")
	source.mutex.Lock()
	source.known["cmake"] = "" // PyPI allows an empty summary, and so does a failure
	source.mutex.Unlock()
	view := frameOf(d.picker.View())
	if strings.Contains(view, "looking up") {
		t.Errorf("a finished lookup must not still say it is looking:\n%s", view)
	}
	// The row is still there and still addable; only the nicety is missing.
	if !strings.Contains(view, "cmake") {
		t.Errorf("the list should be unaffected:\n%s", view)
	}
}

func TestPickerWithoutSummariesNeverAsksAndKeepsItsHeight(t *testing.T) {
	// The apt index carries its descriptions, so it implements no summaries
	// and the field has no line for them.
	withSummaries := newSummaryDriver(t, newSummarySource())
	withSummaries.typeText("cmake")
	apt := newPickerDriver(t, openSampleIndex, "")
	apt.typeText("cmake")
	if apt.picker.summaries() != nil {
		t.Error("the apt index must not offer summaries")
	}
	tall := strings.Count(withSummaries.picker.View(), "\n")
	short := strings.Count(apt.picker.View(), "\n")
	if tall != short+1 {
		t.Errorf("a field with summaries is %d lines and one without %d; want exactly one more", tall, short)
	}
}

func TestPickerKeepsItsHeightWhileASummaryArrives(t *testing.T) {
	source := newSummarySource()
	source.release = make(chan struct{})
	d := newSummaryDriver(t, source)
	d.typeText("cmake")
	looking := strings.Count(d.picker.View(), "\n")
	close(source.release)
	d.press(pressDown)
	d.press(pressUp)
	arrived := strings.Count(d.picker.View(), "\n")
	if looking != arrived {
		t.Errorf("the field is %d lines while looking up and %d once the summary is there", looking, arrived)
	}
}

// TestPickerToggleOffLeavesTheChosenList: with no query the list is the
// chosen names themselves, so one that was just removed has to leave it.
// The answer was always right; the list was not, and Space on the row that
// should have gone put the name back, at the end rather than where it was.
func TestPickerToggleOffLeavesTheChosenList(t *testing.T) {
	answer := "alpha beta gamma"
	picker := newPickerField(context.Background(), form.Field{Key: form.KeyOtherPackages, Title: "Other packages"},
		&answer, func() form.IndexRequest { return form.IndexRequest{Release: "24.04"} }, func() []string { return nil }, unicodeGlyphs)
	picker.Focus()
	picker.refresh()
	if len(picker.rows) != 3 {
		t.Fatalf("rows = %d, want the three chosen names", len(picker.rows))
	}

	picker.cursor = 1 // beta
	picker.toggle()
	if answer != "alpha gamma" {
		t.Errorf("answer = %q, want %q", answer, "alpha gamma")
	}
	if len(picker.rows) != 2 || picker.rows[0].name != "alpha" || picker.rows[1].name != "gamma" {
		t.Fatalf("rows = %v, want the list to match the answer", rowNames(picker))
	}
	// The cursor stays where the person left it rather than jumping home.
	if picker.cursor != 1 {
		t.Errorf("cursor = %d, want 1", picker.cursor)
	}
	// Space on the row now under the cursor removes gamma; before, the row
	// there was the stale beta and Space put beta back.
	picker.toggle()
	if answer != "alpha" {
		t.Errorf("answer = %q, want %q", answer, "alpha")
	}
}

func rowNames(p *pickerField) []string {
	names := make([]string, 0, len(p.rows))
	for _, row := range p.rows {
		names = append(names, row.name)
	}
	return names
}

func TestFormOpensTheIndexWithTheSourcesAnswered(t *testing.T) {
	var asked []form.IndexRequest
	host := noHost
	host.OpenIndex = func(_ context.Context, request form.IndexRequest, _ func(int64, int64)) (form.PackageIndex, error) {
		asked = append(asked, request)
		return sampleUnionIndex{}, nil
	}
	// One source is preselected, as edit does with a recipe that already
	// names it; the other is chosen here, on the page before the picker's,
	// which is the whole point of the order: a source answered a moment ago
	// must be a source the picker searches.
	initial := form.Defaults(noHost)
	initial[form.KeyPPAs] = "deadsnakes/ppa"
	model := newFormModel(context.Background(), form.Fields(host), initial, nil)
	d := &formDriver{driver: newDriver(t, model), model: model}
	d.press(tea.WindowSizeMsg{Width: 80, Height: 24})
	for presses := 0; presses < maxKeyPresses && d.model.pages.GetFocusedField().GetKey() != form.KeySources; presses++ {
		d.press(pressEnter)
	}
	if got := d.model.pages.GetFocusedField().GetKey(); got != form.KeySources {
		t.Fatalf("the sources are not asked before the packages; reached %q", got)
	}
	var catalog form.Field
	for _, field := range form.Fields(noHost) {
		if field.Key == form.KeySources {
			catalog = field
		}
	}
	d.press(pressSpace) // the first of the catalog, whichever it is
	for presses := 0; presses < maxKeyPresses && d.model.pages.GetFocusedField().GetKey() != form.KeyOtherPackages; presses++ {
		d.press(pressEnter)
	}
	if got := d.model.pages.GetFocusedField().GetKey(); got != form.KeyOtherPackages {
		t.Fatalf("never reached the picker; at %q", got)
	}
	if len(asked) != 1 {
		t.Fatalf("the index was opened %d times, want once", len(asked))
	}
	var named []string
	for _, source := range asked[0].Sources {
		if source.URL == "" {
			t.Errorf("%s reached the index with no URL to fetch from", source.Name)
		}
		named = append(named, source.Name)
	}
	if !slices.Contains(named, catalog.Options[0].Value) || !slices.Contains(named, "ppa-deadsnakes-ppa") {
		t.Errorf("the picker asked for %q, want %q and the PPA answered before it", named, catalog.Options[0].Value)
	}
	// And what it searches shows them: the row says which repository the
	// package comes from.
	d.typeText("docker")
	if view := d.model.View(); !strings.Contains(view, "docker · Docker") {
		t.Errorf("the picker does not name the source a row comes from:\n%s", view)
	}
}

// degradedIndex is an index that came back without one of its repositories.
type degradedIndex struct{ sampleIndex }

func (degradedIndex) Sources() ([]string, []string) {
	return []string{"noble", "docker"}, []string{"docker"}
}

func TestPickerTriesARepositoryItCouldNotReadAgain(t *testing.T) {
	var opens int
	opener := func(context.Context, form.IndexRequest, func(int64, int64)) (form.PackageIndex, error) {
		opens++
		if opens == 1 {
			return degradedIndex{}, nil // the vendor was down
		}
		return sampleIndex{}, nil // and is back
	}
	answer := ""
	request := form.IndexRequest{Release: "24.04", Sources: []recipe.Source{{Name: "docker", URL: "https://download.docker.com/linux/ubuntu"}}}
	picker := newPickerField(context.Background(), form.Field{Key: form.KeyOtherPackages, OpenIndex: opener}, &answer, func() form.IndexRequest { return request }, func() []string { return nil }, unicodeGlyphs)
	d := newDriver(t, picker)
	d.settle(picker.Focus())
	// Coming back to the field with the same answers opens it again,
	// because last time a repository was left out of it.
	picker.Blur()
	d.settle(picker.Focus())
	if opens != 2 {
		t.Errorf("opened %d times, want the missing repository tried again", opens)
	}
	// Now that nothing is missing, coming back changes nothing.
	picker.Blur()
	d.settle(picker.Focus())
	if opens != 2 {
		t.Errorf("opened %d times, want a whole index left alone", opens)
	}
}

func TestPythonFieldIsNotReopenedWhenASourceChanges(t *testing.T) {
	// PyPI is one index whatever apt sources the recipe adds. A field that
	// reopened on them would throw away a 9.7 MB download every time one
	// was ticked.
	var opened []form.IndexRequest
	host := noHost
	host.OpenIndex = func(_ context.Context, request form.IndexRequest, _ func(int64, int64)) (form.PackageIndex, error) {
		opened = append(opened, request)
		return sampleIndex{}, nil
	}
	host.OpenPythonIndex = func(_ context.Context, request form.IndexRequest, _ func(int64, int64)) (form.PackageIndex, error) {
		if len(request.Sources) != 0 {
			t.Errorf("the PyPI index was asked for apt sources: %+v", request.Sources)
		}
		return sampleIndex{}, nil
	}
	initial := form.Defaults(noHost)
	initial[form.KeySources] = []string{"docker"}
	model := newFormModel(context.Background(), form.Fields(host), initial, nil)
	d := &formDriver{driver: newDriver(t, model), model: model}
	d.press(tea.WindowSizeMsg{Width: 80, Height: 24})
	for presses := 0; presses < maxKeyPresses && d.model.pages.GetFocusedField().GetKey() != form.KeyPythonPackages; presses++ {
		d.press(pressEnter)
	}
	if got := d.model.pages.GetFocusedField().GetKey(); got != form.KeyPythonPackages {
		t.Fatalf("never reached the Python field; at %q", got)
	}
	// And the apt field, which does search them, was told about the source.
	if len(opened) != 1 || len(opened[0].Sources) != 1 {
		t.Errorf("the apt index was opened with %+v, want the recipe's one source", opened)
	}
}
