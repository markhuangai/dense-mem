package contract

import (
	"sort"
	"unicode"
	"unicode/utf8"
)

type RecallReadInput struct {
	SpaceID     string
	Query       string
	EvidenceIDs []string
	// A non-nil slice preloads groups for candidates and discovered Evidence, including an empty initial pool.
	CandidateEvidenceIDs []string
	Limit                int
}

type RecallEvidenceGroup struct {
	ID      string
	Members []string
}

type RecallOrganization struct {
	Sources     []SourceHandle
	Groups      []RecallEvidenceGroup
	Degradation string
}

func RecallQueryNames(query string) ([]string, bool) {
	query = NormalizeName(query)
	starts, ends := []int{}, []int{}
	inWord := false
	for offset, char := range query {
		if unicode.IsSpace(char) {
			if inWord {
				ends = append(ends, offset)
				inWord = false
			}
		} else if !inWord {
			starts = append(starts, offset)
			inWord = true
		}
	}
	if inWord {
		ends = append(ends, len(query))
	}
	names := map[string]bool{}
	for start := range starts {
		for end := start; end < len(ends); end++ {
			name := query[starts[start]:ends[end]]
			if utf8.RuneCountInString(name) > 128 {
				break
			}
			characters := []rune(name)
			for left := 0; left < len(characters); left++ {
				for right := len(characters); right > left; right-- {
					variant := NormalizeName(string(characters[left:right]))
					if variant != "" {
						names[variant] = true
					}
					if len(names) > MaxDependencyRecords {
						return nil, true
					}
					if !unicode.IsPunct(characters[right-1]) {
						break
					}
				}
				if !unicode.IsPunct(characters[left]) {
					break
				}
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, false
}

func MatchRecallDefinitions(names []string, views []RecordView) []string {
	queryNames, aliases := map[string]bool{}, map[string]map[string]bool{}
	for _, name := range names {
		queryNames[name] = true
	}
	for _, view := range views {
		if !view.Current || view.Definition == nil {
			continue
		}
		for _, alias := range view.Definition.Aliases {
			name := NormalizeName(alias)
			if aliases[name] == nil {
				aliases[name] = map[string]bool{}
			}
			aliases[name][view.ID] = true
		}
		name := NormalizeName(view.Definition.Key)
		if aliases[name] == nil {
			aliases[name] = map[string]bool{}
		}
		aliases[name][view.ID] = true
	}
	result := []string{}
	for _, view := range views {
		if !view.Current || view.Definition == nil {
			continue
		}
		matched := queryNames[NormalizeName(view.Definition.Key)]
		for _, alias := range view.Definition.Aliases {
			name := NormalizeName(alias)
			matched = matched || (queryNames[name] && len(aliases[name]) == 1)
		}
		if matched {
			result = append(result, view.ID)
		}
	}
	sort.Strings(result)
	return result
}
