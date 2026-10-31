package evalharness

type OrganizationMatchingCase struct {
	Case                  OrganizationCase `json:"case"`
	DefinitionKey         string           `json:"definition_key"`
	DefinitionDescription string           `json:"definition_description"`
	DefinitionAliases     []string         `json:"definition_aliases,omitempty"`
	ExpectedCandidate     bool             `json:"expected_candidate"`
}

func OntologyMatchingCohort() []OrganizationMatchingCase {
	pair := func(id, left, right, key, description string, aliases []string, matched, equivalent bool) OrganizationMatchingCase {
		sources := []OrganizationSource{
			{ID: id + ":1", Text: left, Actor: "Atlas", Polarity: "+", Time: "current", EquivalenceKey: id + ":left", FactIDs: []string{id + ":left"}},
			{ID: id + ":2", Text: right, Actor: "Atlas", Polarity: "+", Time: "current", EquivalenceKey: id + ":right", FactIDs: []string{id + ":right"}},
		}
		if equivalent {
			sources[1].EquivalenceKey = sources[0].EquivalenceKey
			sources[1].FactIDs = append([]string(nil), sources[0].FactIDs...)
		}
		return OrganizationMatchingCase{Case: OrganizationCase{ID: id, Sources: sources}, DefinitionKey: key,
			DefinitionDescription: description, DefinitionAliases: aliases, ExpectedCandidate: matched}
	}
	cases := []OrganizationMatchingCase{
		pair("stop_word_only", "Atlas stores data in PostgreSQL.", "Vega builds the aircraft.", "grammarnoise", "of and the in", nil, false, false),
		pair("domain_terms", "Atlas stores data in PostgreSQL.", "Data is stored in PostgreSQL by Atlas.", "relationalcatalog", "PostgreSQL storage", nil, true, true),
		pair("stop_word_name", "The about project stores data in PostgreSQL.", "Data is stored in PostgreSQL by the about project.", "about", "Project data storage", nil, true, true),
		pair("stop_word_alias", "Atlas uses the system named about.", "The system called about is used by Atlas.", "namedplatform", "Named platform", []string{"about"}, true, true),
		pair("short_identifier", "Q stores Atlas records.", "Atlas records are stored by Q.", "q", "Record storage", nil, true, true),
		pair("unicode", "東京 utilise PostgreSQL.", "東京 utilise PostgreSQL.", "internationalcatalog", "東京", nil, true, true),
		pair("polarity", "Atlas uses PostgreSQL.", "Atlas does not use PostgreSQL.", "databasepolarity", "PostgreSQL", nil, true, false),
		pair("time", "Atlas uses PostgreSQL before 2025.", "Atlas uses PostgreSQL after 2025.", "databasetime", "PostgreSQL", nil, true, false),
	}
	cases[0].Case.Sources[1].Actor = "Vega"
	cases[2].Case.Sources[0].Actor, cases[2].Case.Sources[1].Actor = "about", "about"
	cases[6].Case.Sources[1].Polarity = "-"
	cases[7].Case.Sources[0].Time, cases[7].Case.Sources[1].Time = "before 2025", "after 2025"
	return cases
}
