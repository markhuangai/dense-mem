//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type recallAccuracyFixture struct {
	*recallOntologyFixture
	cohort     []evalharness.RecallAccuracyCase
	mapping    evalharness.KnowledgeMapping
	sources    map[string]evalharness.RecallAccuracySource
	evidence   map[string]ontology.SourceHandle
	entities   map[string]string
	predicates map[string]ontology.SourceHandle
	vectors    bool
	preserved  string
}

func newRecallAccuracyFixture(t *testing.T, reverseIDs, vectors bool) *recallAccuracyFixture {
	t.Helper()
	f := &recallAccuracyFixture{recallOntologyFixture: newRecallOntologyFixture(t), cohort: evalharness.RecallAccuracyCohort(),
		mapping: evalharness.KnowledgeMapping{BySourceDocID: map[string]evalharness.Ref{}},
		sources: map[string]evalharness.RecallAccuracySource{}, evidence: map[string]ontology.SourceHandle{},
		entities: map[string]string{}, predicates: map[string]ontology.SourceHandle{}, vectors: vectors}
	var sources []evalharness.RecallAccuracySource
	for _, c := range f.cohort {
		sources = append(sources, c.Sources...)
	}
	if reverseIDs {
		slices.Reverse(sources)
	}
	for index, source := range sources {
		owner := index % 2
		ctx := f.actor(owner, "member")
		id := fmt.Sprintf("66600000-0000-4000-8000-%012x", index+1)
		ingest, err := f.knowledge.CreateIngestForTest(ctx, knowledge.CreateIngestInput{
			TeamID: f.team, OwnerProfileID: f.owners[owner], SpaceID: f.space, SpaceGeneration: f.generation,
			IdempotencyKey: source.ID, RequestHash: sha256Hex(source.Text),
			Evidence: []knowledge.EvidenceInput{{FragmentID: id, Content: source.Text, SourceType: "document", Authority: "primary", ForceInsert: true}},
		})
		require.NoError(t, err)
		require.Equal(t, id, ingest.Evidence[0].FragmentID)
		f.sources[id] = source
		f.evidence[source.ID] = ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: id, Version: 1}
		f.mapping.BySourceDocID[source.ID] = evalharness.Ref{Type: "evidence", ID: id, SourceDocID: source.ID}
		entity := func(kind, name string) string {
			key := kind + ":" + name
			if f.entities[key] == "" {
				f.entities[key] = createSemanticEntity(t, ctx, f.knowledge, f.team, f.owners[owner], kind, name).EntityID
			}
			return f.entities[key]
		}
		subject, object := entity(source.SubjectKind, source.Subject), entity(source.ObjectKind, source.Object)
		predicate, err := f.knowledge.EnsureSemanticReviewPredicateCandidate(ctx, knowledge.EnsureSemanticPredicateCandidateInput{
			TeamID: f.team, OwnerProfileID: f.owners[owner], Predicate: source.Predicate, RelationshipKind: "state", SubjectKind: source.SubjectKind, ObjectKind: source.ObjectKind,
		})
		require.NoError(t, err)
		f.predicates[source.ID] = ontology.SourceHandle{Kind: ontology.PredicateSource, ID: predicate.PredicateKey, Version: int64(predicate.Version)}
		decision := applySemanticDecision(t, ctx, f.knowledge, ApplyRelationshipDecisionInput{
			TeamID: f.team, OwnerProfileID: f.owners[owner], IngestID: ingest.IngestID,
			SubjectEntityID: subject, PredicateKey: predicate.PredicateKey, PredicateVersion: predicate.Version, ObjectEntityID: object,
			Polarity: source.Polarity, ValidFrom: accuracyTime(t, source.ValidFrom), ValidTo: accuracyTime(t, source.ValidTo),
			Support: &EvidenceSupportInput{FragmentID: id, SourceGroupKey: source.ID, SpanStart: 0, SpanEnd: utf8.RuneCountInString(source.Text), Quote: source.Text, Authority: "primary"},
		})
		require.NotNil(t, decision.Relationship)
		for documentIndex, ref := range []ontology.SourceHandle{f.evidence[source.ID], {Kind: ontology.RelationshipSource, ID: decision.Relationship.RelationshipID, Version: int64(decision.Relationship.Version)}} {
			document, err := f.search.UpsertSearchDocument(ctx, knowledge.UpsertSearchDocumentInput{
				TeamID: f.team, OwnerProfileID: f.owners[owner], SpaceID: f.space, SpaceGeneration: f.generation,
				SourceKind: string(ref.Kind), SourceID: ref.ID, SourceVersion: ref.Version, DocumentText: source.Text,
			})
			require.NoError(t, err)
			stableID := fmt.Sprintf("66610000-0000-4000-8000-%012x", index*2+documentIndex+1)
			f.fixSearchDocumentIdentity(t, document.SearchDocumentID, stableID)
			if vectors && document.SearchState == "pending" {
				completeSearchDocumentsForTest(t, f.search, f.team, map[string][]float32{stableID: {1, 0, 0}})
			}
		}
	}
	// Equal timestamps leave the deliberately controlled document UUIDs as the full-text and vector tie breakers.
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE evidence_fragments SET created_at='2026-01-01T00:00:00Z' WHERE team_id=?::uuid`, f.team).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE search_documents SET created_at='2026-01-01T00:00:00Z', updated_at='2026-01-01T00:00:00Z' WHERE team_id=?::uuid`, f.team).Error
	}))
	f.preserved = f.canonicalSnapshot(t)
	for _, c := range f.cohort {
		definition := ontology.Record{ID: uuid.NewString(), Kind: ontology.Kind(c.Concept.Kind), Definition: &ontology.Definition{
			Key: c.Concept.Key, Label: c.Concept.Key, Aliases: c.Concept.Aliases}}
		records := []ontology.Record{definition}
		seen := map[string]bool{}
		for _, sourceID := range c.Concept.SourceIDs {
			handle := f.evidence[sourceID]
			if definition.Kind == ontology.PredicateConcept {
				handle = f.predicates[sourceID]
			}
			key := ontology.SourceKey(handle)
			if !seen[key] {
				records = append(records, f.assignment(t, definition.ID, handle))
				seen[key] = true
			}
		}
		f.publication(t, records...)
	}
	require.Equal(t, f.preserved, f.canonicalSnapshot(t))
	return f
}

func (f *recallAccuracyFixture) fixSearchDocumentIdentity(t testing.TB, oldID, stableID string) {
	t.Helper()
	// Preserve newly queued fixture jobs while re-keying their non-deferrable parent before any worker can consume them.
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		var jobs string
		if err := tx.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(job))::text,'[]') FROM embedding_jobs job WHERE team_id=?::uuid AND search_document_id=?::uuid`, f.team, oldID).Scan(&jobs).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM embedding_jobs WHERE team_id=?::uuid AND search_document_id=?::uuid`, f.team, oldID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE search_documents SET search_document_id=?::uuid WHERE team_id=?::uuid AND search_document_id=?::uuid`, stableID, f.team, oldID).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO embedding_jobs SELECT (jsonb_populate_record(NULL::embedding_jobs, job || jsonb_build_object('search_document_id', ?::uuid))).* FROM jsonb_array_elements(?::jsonb) AS job`, stableID, jobs).Error
	}))
}

func accuracyTime(t testing.TB, value string) *time.Time {
	t.Helper()
	if value == "" {
		return nil
	}
	instant, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return &instant
}

func (f *recallAccuracyFixture) canonicalSnapshot(t testing.TB) string {
	t.Helper()
	var evidence, relationships string
	require.NoError(t, f.rls.WithTeamTx(f.actor(2, "member"), f.app, f.team, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(e) ORDER BY fragment_id)::text,'[]') FROM evidence_fragments e WHERE team_id=?::uuid`, f.team).Scan(&evidence).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY relationship_id)::text,'[]') FROM relationship_records r WHERE team_id=?::uuid`, f.team).Scan(&relationships).Error
	}))
	return sha256Hex(evidence + relationships)
}

type recallAccuracyObservation struct {
	CaseID          string                     `json:"case_id"`
	Variant         string                     `json:"variant"`
	Organization    bool                       `json:"ontology_enabled"`
	RankedSources   [][]string                 `json:"ranked_sources"`
	Score           evalharness.RetrievalScore `json:"score"`
	SearchState     string                     `json:"search_state"`
	Degradations    []string                   `json:"degradations"`
	CanonicalIntact bool                       `json:"canonical_preserved"`
}

func (f *recallAccuracyFixture) observe(t *testing.T, c evalharness.RecallAccuracyCase, organization bool, variant string, expand bool) recallAccuracyObservation {
	t.Helper()
	input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: c.Case.Query, Limit: c.Case.Limit,
		OrganizationEnabled: organization, ValidAt: accuracyTime(t, c.Case.ValidAt)}
	if f.vectors {
		input.QueryEmbedding = []float32{1, 0, 0}
	}
	if expand {
		input.Query = ""
		input.QueryEmbedding = nil
		first := c.Sources[0]
		input.ExpandFromEntityIDs = []string{f.entities[first.SubjectKind+":"+first.Subject]}
	}
	result, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	observation := recallAccuracyObservation{CaseID: c.Case.CaseID, Variant: variant, Organization: organization,
		RankedSources: [][]string{}, SearchState: result.SearchState, Degradations: degradationCodes(result.Degradations)}
	trace := evalharness.RecallTrace{CaseID: c.Case.CaseID, Query: input.Query, RankedRefs: []evalharness.Ref{}}
	for index, hit := range result.Results {
		require.Equal(t, index+1, hit.Rank)
		require.Contains(t, f.sources, hit.EvidenceID)
		require.Equal(t, f.sources[hit.EvidenceID].Text, hit.Context)
		ids := append([]string{hit.EvidenceID}, hit.EquivalentEvidenceIDs...)
		var sources []string
		for _, id := range ids {
			require.Contains(t, f.sources, id, "unmapped or foreign evidence")
			sources = append(sources, f.sources[id].ID)
		}
		observation.RankedSources = append(observation.RankedSources, sources)
		trace.RankedRefs = append(trace.RankedRefs, evalharness.Ref{Type: "evidence", ID: hit.EvidenceID, Rank: hit.Rank, EquivalentEvidenceIDs: hit.EquivalentEvidenceIDs})
	}
	observation.Score = evalharness.ScoreTrace(c.Case.CaseID, c.Case.Limit, c.QRel, trace, f.mapping)
	require.Empty(t, observation.Score.UnmappedSourceRefs)
	observation.CanonicalIntact = f.preserved == f.canonicalSnapshot(t)
	require.True(t, observation.CanonicalIntact)
	encoded, err := json.Marshal(observation)
	require.NoError(t, err)
	t.Logf("RECALL_ACCURACY_CASE %s", encoded)
	return observation
}
