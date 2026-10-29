package evalharness

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const RecallAccuracySeedID = "recall_accuracy_v1"

type RecallAccuracySource struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	Subject     string `json:"subject"`
	SubjectKind string `json:"subject_kind"`
	Predicate   string `json:"predicate"`
	Object      string `json:"object"`
	ObjectKind  string `json:"object_kind"`
	Polarity    string `json:"polarity"`
	ValidFrom   string `json:"valid_from,omitempty"`
	ValidTo     string `json:"valid_to,omitempty"`
}

type RecallAccuracyJudgment struct {
	CaseID      string `json:"case_id"`
	SourceDocID string `json:"source_doc_id"`
	Relevant    bool   `json:"relevant"`
	Reason      string `json:"reason"`
	SpanStart   int    `json:"span_start"`
	SpanEnd     int    `json:"span_end"`
	Quote       string `json:"quote"`
}

type RecallAccuracyConcept struct {
	Kind      string   `json:"kind"`
	Key       string   `json:"key"`
	Aliases   []string `json:"aliases,omitempty"`
	SourceIDs []string `json:"source_ids"`
}

type RecallAccuracyCase struct {
	Case      Case                     `json:"case"`
	Sources   []RecallAccuracySource   `json:"sources"`
	QRel      QRel                     `json:"qrel"`
	Judgments []RecallAccuracyJudgment `json:"judgments"`
	Concept   RecallAccuracyConcept    `json:"concept"`
}

func (source RecallAccuracySource) CorpusItem() CorpusItem {
	proposal := map[string]any{
		"ref": "relationship", "evidence_indices": []int{0}, "polarity": source.Polarity,
		"subject":   map[string]any{"name": source.Subject, "entity_kind": source.SubjectKind},
		"predicate": map[string]any{"proposed_key": source.Predicate},
		"object":    map[string]any{"entity": map[string]any{"name": source.Object, "entity_kind": source.ObjectKind}},
	}
	if source.ValidFrom != "" {
		proposal["valid_from"] = source.ValidFrom
	}
	if source.ValidTo != "" {
		proposal["valid_to"] = source.ValidTo
	}
	return CorpusItem{SourceDocID: source.ID, Content: source.Text, SourceDataset: RecallAccuracySeedID,
		SourceType: "document", Authority: "primary", Relationships: []any{proposal}}
}

func accuracySource(subject, predicate, object, text string) RecallAccuracySource {
	kindAndName := func(value, fallback string) (string, string) {
		if kind, name, ok := strings.Cut(value, ":"); ok {
			return kind, name
		}
		return fallback, value
	}
	subjectKind, subjectName := kindAndName(subject, "project")
	objectKind, objectName := kindAndName(object, "product")
	return RecallAccuracySource{Text: text, Subject: subjectName, SubjectKind: subjectKind,
		Predicate: predicate, Object: objectName, ObjectKind: objectKind, Polarity: "+"}
}

func accuracyCase(id, family, query, reason string, relevant, irrelevant []RecallAccuracySource) RecallAccuracyCase {
	id = RecallAccuracySeedID + ":" + id
	c := RecallAccuracyCase{Case: Case{CaseID: id, Query: query, Limit: 10, TaskType: "retrieval",
		Slices: []string{family, "adversarial"}}, QRel: QRel{CaseID: id}}
	c.Sources = append(c.Sources, relevant...)
	c.Sources = append(c.Sources, irrelevant...)
	observatory := strings.ReplaceAll(id, ":", " ") + " Observatory"
	c.Sources = append(c.Sources, accuracySource(observatory, "records", "Rainfall", observatory+" records Rainfall."))
	for i := range c.Sources {
		source := &c.Sources[i]
		source.ID = fmt.Sprintf("%s:source:%02d", id, i+1)
		if i == len(c.Sources)-1 {
			continue
		}
		good := i < len(relevant)
		why := reason
		if good {
			why = "Direct support for the query: " + query
		}
		ref := Ref{Type: "evidence", SourceDocID: source.ID, Reason: why}
		if good {
			ref.Grade = 1
			c.QRel.RequiredRefs = append(c.QRel.RequiredRefs, ref)
		} else {
			c.QRel.BadRefs = append(c.QRel.BadRefs, ref)
		}
		c.Judgments = append(c.Judgments, RecallAccuracyJudgment{CaseID: id, SourceDocID: source.ID,
			Relevant: good, Reason: why, SpanEnd: utf8.RuneCountInString(source.Text), Quote: source.Text})
		c.Concept.SourceIDs = append(c.Concept.SourceIDs, source.ID)
	}
	c.Concept.Kind, c.Concept.Key = "topic", relevant[0].Subject
	return c
}

// RecallAccuracyCohort keeps fixed judgments separate from corpus and ontology fixture inputs.
func RecallAccuracyCohort() []RecallAccuracyCase {
	cohort := accuracyPredicateCases()
	cohort = append(cohort, accuracyNameCases()...)
	cohort = append(cohort, accuracyHubCases()...)
	cohort = append(cohort, accuracySynonymCases()...)
	cohort = append(cohort, accuracyTemporalCases()...)
	return append(cohort, accuracyMultihopCases()...)
}

func accuracyPredicateCases() []RecallAccuracyCase {
	rows := []struct{ id, subject, predicate, alias, object, other, query string }{
		{"uses", "Atlas", "uses", "use", "PostgreSQL", "Lyra", "What database does Atlas use?"},
		{"works_on", "person:Amara", "works_on", "work on", "project:Beacon", "person:Borin", "Which project does Amara work on?"},
		{"located_in", "Cedar", "located_in", "located", "place:Oslo", "Cypress", "Where is Cedar located?"},
		{"meaningful_predicate", "Dahlia", "encrypts", "encrypt", "Backups", "Dogwood", "Which projects encrypt Backups?"},
	}
	var result []RecallAccuracyCase
	for _, row := range rows {
		good := accuracySource(row.subject, row.predicate, row.object, "")
		good.Text = fmt.Sprintf("%s %s %s.", good.Subject, strings.ReplaceAll(row.predicate, "_", " "), good.Object)
		bad := accuracySource(row.other, row.predicate, "Logs", "")
		bad.Text = fmt.Sprintf("%s %s Logs.", bad.Subject, strings.ReplaceAll(row.predicate, "_", " "))
		c := accuracyCase("predicate_"+row.id, "generic_predicates", row.query,
			"The shared predicate does not make this different subject or object relevant.", []RecallAccuracySource{good}, []RecallAccuracySource{bad})
		c.Concept.Kind, c.Concept.Key = "predicate_concept", row.predicate
		c.Concept.Aliases = []string{row.alias}
		result = append(result, c)
	}
	return result
}

func accuracyNameCases() []RecallAccuracyCase {
	rows := []struct{ id, subject, other, query string }{
		{"mercury", "Mercury Billing", "Mercury Weather", "What database does Mercury Billing use?"},
		{"atlas", "Atlas Navigation", "Atlas Publishing", "What database does Atlas Navigation use?"},
		{"common_word", "May", "Mayflower", "What database does the May project use?"},
		{"same_name", "project:Phoenix", "organization:Phoenix", "What does project Phoenix use for its database?"},
	}
	var result []RecallAccuracyCase
	for _, row := range rows {
		good := accuracySource(row.subject, "uses", "SQLite", "")
		good.Text = "The " + good.SubjectKind + " " + good.Subject + " uses SQLite for its database."
		bad := accuracySource(row.other, "uses", "MongoDB", "")
		bad.Text = "The " + bad.SubjectKind + " " + bad.Subject + " uses MongoDB for its database."
		c := accuracyCase("name_"+row.id, "ambiguous_names", row.query,
			"A common token or identical name with a different entity kind identifies a different subject.", []RecallAccuracySource{good}, []RecallAccuracySource{bad})
		if alias := strings.Fields(good.Subject)[0]; alias != c.Concept.Key {
			c.Concept.Aliases = []string{alias}
		}
		result = append(result, c)
	}
	return result
}

func accuracyHubCases() []RecallAccuracyCase {
	var result []RecallAccuracyCase
	for _, name := range []string{"Elm", "Fir", "Ginkgo", "Hazel"} {
		good := accuracySource(name, "uses", "Redis", name+" uses Redis for its cache.")
		bad := []RecallAccuracySource{accuracySource(name, "uses", "HubPlatform", name+" uses HubPlatform for billing only.")}
		for i := 1; i <= 12; i++ {
			other := fmt.Sprintf("%s Neighbor %02d", name, i)
			bad = append(bad, accuracySource(other, "uses", "HubPlatform", other+" uses HubPlatform for billing only."))
		}
		c := accuracyCase("hub_"+strings.ToLower(name), "graph_hubs", "What cache does "+name+" use?",
			"Billing edges and other projects attached to the shared hub do not answer this cache query.", []RecallAccuracySource{good}, bad)
		c.Concept.Key = name + " platform"
		c.Concept.Aliases = []string{name, "HubPlatform"}
		result = append(result, c)
	}
	return result
}

func accuracySynonymCases() []RecallAccuracyCase {
	rows := []struct{ id, name, object, alias string }{
		{"database", "Iris", "PostgreSQL", "relational persistence"},
		{"cache", "Juniper", "Memcached", "temporary key storage"},
		{"ownership", "Kestrel", "Orchid", "responsible maintainer"},
		{"acronym", "Linden", "IdentityGateway", "single sign on"},
	}
	var result []RecallAccuracyCase
	for _, row := range rows {
		good := accuracySource(row.name, "uses", row.object, row.name+" uses "+row.object+".")
		alias := good
		alias.Text = row.object + " is used by " + row.name + "."
		if row.id == "ownership" {
			good = accuracySource(row.name, "maintained_by", "organization:"+row.object, row.name+" is maintained by "+row.object+".")
			alias = good
			alias.Text = row.object + " maintains " + row.name + "."
		}
		bad := accuracySource(row.name+" Archive", "uses", "Tape", row.name+" Archive uses Tape for offline backups.")
		c := accuracyCase("synonym_"+row.id, "synonyms", row.alias,
			"The archive's backup medium is unrelated to the requested service alias.", []RecallAccuracySource{good, alias}, []RecallAccuracySource{bad})
		c.Concept.Key = row.name + " service"
		c.Concept.Aliases = []string{row.alias}
		c.Concept.SourceIDs = c.Concept.SourceIDs[:2]
		result = append(result, c)
	}
	return result
}

func accuracyTemporalCases() []RecallAccuracyCase {
	var result []RecallAccuracyCase
	for _, row := range []struct {
		id, name string
		negative bool
	}{{"positive", "Maple", false}, {"negative", "Nettle", true}} {
		positive := accuracySource(row.name, "uses", "Cassandra", row.name+" uses Cassandra.")
		negative := positive
		negative.Polarity, negative.Text = "-", row.name+" does not use Cassandra."
		good, bad, query := positive, negative, "Evidence that "+row.name+" uses Cassandra."
		if row.negative {
			good, bad, query = negative, positive, "Evidence that "+row.name+" does not use Cassandra."
		}
		result = append(result, accuracyCase("polarity_"+row.id, "polarity_time", query,
			"The opposite polarity does not support the queried assertion.", []RecallAccuracySource{good}, []RecallAccuracySource{bad}))
	}
	for _, row := range []struct{ id, name, date string }{{"current", "Olive", "2026-06-01T00:00:00Z"}, {"past", "Poplar", "2025-06-01T00:00:00Z"}} {
		old := accuracySource(row.name, "uses", "MySQL", row.name+" uses MySQL from 2025-01-01 until 2026-01-01.")
		old.ValidFrom, old.ValidTo = "2025-01-01T00:00:00Z", "2026-01-01T00:00:00Z"
		current := accuracySource(row.name, "uses", "PostgreSQL", row.name+" uses PostgreSQL from 2026-01-01.")
		current.ValidFrom = "2026-01-01T00:00:00Z"
		good, bad := current, old
		if row.id == "past" {
			good, bad = old, current
		}
		c := accuracyCase("time_"+row.id, "polarity_time", "What database does "+row.name+" use on "+row.date[:10]+"?",
			"This source describes a different validity interval.", []RecallAccuracySource{good}, []RecallAccuracySource{bad})
		c.Case.ValidAt = row.date
		result = append(result, c)
	}
	return result
}

func accuracyMultihopCases() []RecallAccuracyCase {
	var result []RecallAccuracyCase
	for _, row := range []struct{ person, project, database string }{{"Rhea", "Quartz", "PostgreSQL"}, {"Soren", "Rowan", "Redis"}, {"Tara", "Spruce", "SQLite"}, {"Uma", "Willow", "MariaDB"}} {
		good := []RecallAccuracySource{
			accuracySource("person:"+row.person, "works_on", "project:"+row.project, row.person+" works on "+row.project+"."),
			accuracySource(row.project, "uses", row.database, row.project+" uses "+row.database+"."),
		}
		bad := []RecallAccuracySource{
			accuracySource("person:"+row.person+" Namesake", "works_on", "project:"+row.project+" Archive", row.person+" Namesake works on "+row.project+" Archive."),
			accuracySource(row.project+" Archive", "uses", "Tape", row.project+" Archive uses Tape."),
		}
		c := accuracyCase("multihop_"+strings.ToLower(row.person), "multihop", "What storage does the project that "+row.person+" works on use?",
			"The namesake's disconnected project chain does not answer the requested person's query.", good, bad)
		c.Concept.Key, c.Concept.Aliases = row.project+" storage", []string{row.person, row.project}
		result = append(result, c)
	}
	return result
}
