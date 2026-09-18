package index

import (
	"sort"
	"strings"
)

// The ranks of a match, best first.
const (
	rankExact       = iota // the name is the query
	rankPrefix             // the name starts with it
	rankName               // the name contains it
	rankDescription        // only the description does
	rankCount
)

// Search returns the packages matching query, best first, at most limit of
// them, and how many matched in all. The query is a substring, in any case,
// of the name or the one-line description; a name match ranks above a
// description match, a name that starts with the query above one that
// merely contains it, and the exact name first. Inside a rank shorter names
// come first, so cmake precedes cmake-data precedes extra-cmake-modules.
// section, when not empty, keeps only that archive section. An empty query
// matches everything, in name order: a section, browsed.
//
// It is a plain scan, 2 to 4 ms over a whole release (the spike), so it
// can run on every key.
func (x *Index) Search(query, section string, limit int) (matches []Entry, total int) {
	query = strings.ToLower(strings.TrimSpace(query))
	var ranked [rankCount][]int
	for position, lowered := range x.lowered {
		if section != "" && x.entries[position].Section != section {
			continue
		}
		rank, isMatch := rankOf(lowered, query)
		if !isMatch {
			continue
		}
		ranked[rank] = append(ranked[rank], position)
		total++
	}
	for rank, positions := range ranked {
		if len(matches) >= limit {
			break
		}
		if query != "" && (rank == rankPrefix || rank == rankName) {
			sort.SliceStable(positions, func(i, j int) bool {
				return len(x.entries[positions[i]].Name) < len(x.entries[positions[j]].Name)
			})
		}
		for _, position := range positions {
			if len(matches) >= limit {
				break
			}
			matches = append(matches, x.entries[position])
		}
	}
	return matches, total
}

// rankOf says whether an entry matches query, and how well.
func rankOf(lowered loweredEntry, query string) (int, bool) {
	switch {
	case query == "":
		return rankName, true
	case lowered.name == query:
		return rankExact, true
	case strings.HasPrefix(lowered.name, query):
		return rankPrefix, true
	case strings.Contains(lowered.name, query):
		return rankName, true
	case strings.Contains(lowered.description, query):
		return rankDescription, true
	}
	return 0, false
}

// SectionsMatching returns the sections that hold matches for query, with
// how many each holds, largest first: what the section list offers.
func (x *Index) SectionsMatching(query string) []SectionCount {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return x.Sections()
	}
	counts := map[string]int{}
	for position, lowered := range x.lowered {
		if _, isMatch := rankOf(lowered, query); isMatch {
			counts[x.entries[position].Section]++
		}
	}
	return sortedSections(counts)
}

// Sections returns every section of the release, largest first.
func (x *Index) Sections() []SectionCount {
	return append([]SectionCount(nil), x.sections...)
}

// sortedSections orders section counts largest first, then by name.
func sortedSections(counts map[string]int) []SectionCount {
	sections := make([]SectionCount, 0, len(counts))
	for name, count := range counts {
		sections = append(sections, SectionCount{Name: name, Count: count})
	}
	sort.Slice(sections, func(i, j int) bool {
		if sections[i].Count != sections[j].Count {
			return sections[i].Count > sections[j].Count
		}
		return sections[i].Name < sections[j].Name
	})
	return sections
}

// Lookup returns the package of exactly this name. It is a binary search,
// where Search is a scan: describing two hundred chosen names, as capture
// produces them, must not cost two hundred scans.
func (x *Index) Lookup(name string) (Entry, bool) {
	position := sort.Search(len(x.entries), func(i int) bool { return x.entries[i].Name >= name })
	if position < len(x.entries) && x.entries[position].Name == name {
		return x.entries[position], true
	}
	return Entry{}, false
}

// Has reports whether the release has a package of exactly this name.
func (x *Index) Has(name string) bool {
	_, isThere := x.Lookup(name)
	return isThere
}

// Nearest returns up to limit names close to a name the release lacks, the
// closest first: what "did you mean" offers. Close is an edit distance of
// at most two, three for a name over twelve characters, which is what a
// slip of the fingers produces and a different package does not.
func (x *Index) Nearest(name string, limit int) []string {
	name = strings.ToLower(name)
	bound := 2
	if len(name) > 12 {
		bound = 3
	}
	type candidate struct {
		name     string
		distance int
	}
	var candidates []candidate
	// Two rows, long enough for the longest name that passes the length check.
	rows := [2][]int{make([]int, len(name)+bound+1), make([]int, len(name)+bound+1)}
	for position, lowered := range x.lowered {
		if difference := len(lowered.name) - len(name); difference > bound || -difference > bound {
			continue
		}
		if distance := editDistance(name, lowered.name, bound, rows); distance <= bound {
			candidates = append(candidates, candidate{name: x.entries[position].Name, distance: distance})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].distance < candidates[j].distance })
	var nearest []string
	for _, found := range candidates {
		if len(nearest) >= limit {
			break
		}
		nearest = append(nearest, found.name)
	}
	return nearest
}

// editDistance is the Levenshtein distance between two ASCII-ish strings,
// or bound+1 as soon as it is known to exceed bound: package names are
// short and nearly all of them are far from any given one, so most calls
// end in their first rows. rows are two scratch slices of at least
// len(right)+1 each, so that a scan of the archive allocates twice.
func editDistance(left, right string, bound int, rows [2][]int) int {
	previous, current := rows[0][:len(right)+1], rows[1][:len(right)+1]
	for column := range previous {
		previous[column] = column
	}
	for row := 1; row <= len(left); row++ {
		current[0] = row
		smallest := current[0]
		for column := 1; column <= len(right); column++ {
			cost := 1
			if left[row-1] == right[column-1] {
				cost = 0
			}
			current[column] = min(previous[column]+1, current[column-1]+1, previous[column-1]+cost)
			smallest = min(smallest, current[column])
		}
		if smallest > bound {
			return bound + 1
		}
		previous, current = current, previous
	}
	return previous[len(right)]
}
