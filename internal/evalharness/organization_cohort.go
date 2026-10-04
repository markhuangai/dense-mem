package evalharness

type OrganizationSource struct {
	ID             string   `json:"id"`
	Text           string   `json:"text"`
	Actor          string   `json:"actor"`
	Polarity       string   `json:"polarity"`
	Time           string   `json:"time"`
	Qualification  string   `json:"qualification"`
	FactIDs        []string `json:"fact_ids"`
	EquivalenceKey string   `json:"equivalence_key"`
}

type OrganizationCase struct {
	ID       string               `json:"id"`
	Override string               `json:"override,omitempty"`
	Sources  []OrganizationSource `json:"sources"`
}

// OntologyOrganizationCohort supplies synthetic manual judgments, never factual evidence for Remember.
func OntologyOrganizationCohort() []OrganizationCase {
	source := func(id, text, actor, polarity, time, qualification, key string, facts ...string) OrganizationSource {
		return OrganizationSource{ID: id, Text: text, Actor: actor, Polarity: polarity, Time: time, Qualification: qualification, EquivalenceKey: key, FactIDs: facts}
	}
	return []OrganizationCase{
		{ID: "exact_duplicates", Sources: []OrganizationSource{
			source("exact:1", "Atlas uses PostgreSQL.", "Atlas", "+", "current", "", "exact", "postgres"),
			source("exact:2", "Atlas uses PostgreSQL.", "Atlas", "+", "current", "", "exact", "postgres"),
			source("exact:3", "Atlas uses PostgreSQL.", "Atlas", "+", "current", "", "exact", "postgres"),
		}},
		{ID: "paraphrases", Sources: []OrganizationSource{
			source("paraphrase:1", "Atlas stores data in PostgreSQL.", "Atlas", "+", "current", "", "storage", "postgres"),
			source("paraphrase:2", "PostgreSQL is Atlas's data store.", "Atlas", "+", "current", "", "storage", "postgres"),
		}},
		{ID: "partial_overlap", Sources: []OrganizationSource{
			source("partial:1", "Atlas uses PostgreSQL and Redis.", "Atlas", "+", "current", "", "both", "postgres", "redis"),
			source("partial:2", "Atlas uses PostgreSQL.", "Atlas", "+", "current", "", "postgres-only", "postgres"),
		}},
		{ID: "different_actors", Sources: []OrganizationSource{
			source("actor:1", "I use PostgreSQL.", "Ada", "+", "current", "", "ada", "ada-postgres"),
			source("actor:2", "I use PostgreSQL.", "Bo", "+", "current", "", "bo", "bo-postgres"),
		}},
		{ID: "polarity", Sources: []OrganizationSource{
			source("polarity:1", "Atlas uses PostgreSQL.", "Atlas", "+", "current", "", "positive", "uses"),
			source("polarity:2", "Atlas does not use PostgreSQL.", "Atlas", "-", "current", "", "negative", "does-not-use"),
		}},
		{ID: "temporal_scope", Sources: []OrganizationSource{
			source("time:1", "Atlas uses PostgreSQL.", "Atlas", "+", "2025", "", "2025", "postgres-2025"),
			source("time:2", "Atlas uses PostgreSQL.", "Atlas", "+", "2026", "", "2026", "postgres-2026"),
		}},
		{ID: "qualifications", Sources: []OrganizationSource{
			source("qualified:1", "Atlas uses PostgreSQL in production.", "Atlas", "+", "current", "production", "production", "production-postgres"),
			source("qualified:2", "Atlas uses PostgreSQL for tests only.", "Atlas", "+", "current", "tests-only", "tests", "tests-postgres"),
		}},
		{ID: "manager_separation", Override: "keep_separate", Sources: []OrganizationSource{
			source("separate:1", "Atlas uses PostgreSQL.", "Atlas", "+", "current", "", "kept-first", "postgres"),
			source("separate:2", "Atlas uses PostgreSQL.", "Atlas", "+", "current", "", "kept-second", "postgres"),
		}},
		{ID: "manager_grouping", Override: "group_together", Sources: []OrganizationSource{
			source("together:1", "Atlas uses PostgreSQL.", "Atlas", "+", "current", "", "together", "postgres"),
			source("together:2", "Atlas uses PostgreSQL.", "Atlas", "+", "current", "", "together", "postgres"),
		}},
	}
}
