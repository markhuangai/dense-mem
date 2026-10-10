package evalharness

type OrganizationFollowupCase struct {
	Case            OrganizationCase `json:"case"`
	CreatedAt       []string         `json:"created_at"`
	Timezone        string           `json:"timezone"`
	StaleVocabulary bool             `json:"stale_vocabulary"`
}

func OntologyOrganizationFollowupCohort() []OrganizationFollowupCase {
	source := func(id, text, actor, key, fact string) OrganizationSource {
		return OrganizationSource{ID: id, Text: text, Actor: actor, Polarity: "+", Time: "submitted calendar context", FactIDs: []string{fact}, EquivalenceKey: key}
	}
	pair := func(id, left, right, leftActor, rightActor, leftKey, rightKey, leftFact, rightFact, timezone, first, second string, stale bool) OrganizationFollowupCase {
		return OrganizationFollowupCase{Case: OrganizationCase{ID: id, Sources: []OrganizationSource{source(id+":1", left, leftActor, leftKey, leftFact), source(id+":2", right, rightActor, rightKey, rightFact)}}, CreatedAt: []string{first, second}, Timezone: timezone, StaleVocabulary: stale}
	}
	const instant = "2026-10-08T12:00:00Z"
	return []OrganizationFollowupCase{
		pair("resolved_calendar", "Atlas launched today.", "Atlas was launched today.", "Atlas", "Atlas", "launch", "launch", "atlas-launch", "atlas-launch", "UTC", instant, instant, false),
		pair("unresolved_calendar", "Atlas launched today.", "Atlas launched today.", "Atlas", "Atlas", "unresolved-first", "unresolved-second", "first-calendar-launch", "second-calendar-launch", "", instant, instant, false),
		pair("owner_relative_calendar", "I launched today.", "I launched today.", "Ada", "Bo", "ada-launch", "bo-launch", "ada-launch", "bo-launch", "UTC", instant, instant, false),
		pair("different_calendar_day", "Atlas launched today.", "Atlas launched yesterday.", "Atlas", "Atlas", "today-launch", "yesterday-launch", "today-launch", "yesterday-launch", "UTC", instant, instant, false),
		pair("different_creation_date", "Atlas launched today.", "Atlas launched today.", "Atlas", "Atlas", "first-date", "second-date", "first-date-launch", "second-date-launch", "UTC", instant, "2026-10-09T12:00:00Z", false),
		pair("stale_vocabulary_window", "Atlas stores its relational data in PostgreSQL.", "PostgreSQL is Atlas's relational data store.", "Atlas", "Atlas", "storage", "storage", "atlas-postgres", "atlas-postgres", "", instant, instant, true),
	}
}
