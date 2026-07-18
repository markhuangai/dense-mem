package repository

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV2SemanticRepositoryFailsClosedWithoutDependencies(t *testing.T) {
	_, err := (*V2SemanticRepositoryImpl)(nil).CreateEntity(context.Background(), V2CreateEntityInput{
		TeamID:         "f9f8b369-3240-44b8-a9b1-64ad3b56bcab",
		OwnerProfileID: "5d285966-87d9-47b1-b1f7-1c7bb1415de4",
		EntityKind:     "person",
		CanonicalName:  "Mark",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database is required")

	repo := &V2SemanticRepositoryImpl{db: &gorm.DB{}}
	_, err = repo.CreateEntity(context.Background(), V2CreateEntityInput{
		TeamID:         "f9f8b369-3240-44b8-a9b1-64ad3b56bcab",
		OwnerProfileID: "5d285966-87d9-47b1-b1f7-1c7bb1415de4",
		EntityKind:     "person",
		CanonicalName:  "Mark",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rls helper is required")
}

func TestV2SemanticEntitiesAllowHomonymsAndTypedValuesDeduplicate(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "semantic-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewV2SemanticRepository(appDB, rls)

	first, err := repo.CreateEntity(ctx, V2CreateEntityInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EntityKind:     "person",
		CanonicalName:  "Mark",
		IdentityContext: map[string]any{
			"team": "Dense-Mem",
		},
	})
	require.NoError(t, err)

	second, err := repo.CreateEntity(ctx, V2CreateEntityInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EntityKind:     "person",
		CanonicalName:  "Mark",
		IdentityContext: map[string]any{
			"team": "Finance",
		},
	})
	require.NoError(t, err)
	require.NotEqual(t, first.EntityID, second.EntityID)

	aliasID, err := repo.AddEntityName(ctx, V2AddEntityNameInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EntityID:       first.EntityID,
		DisplayName:    "M. Huang",
		NameKind:       "alias",
	})
	require.NoError(t, err)
	require.NotEmpty(t, aliasID)

	var markNameCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM entity_names
			WHERE team_id = ?::uuid
			  AND normalized_name = 'mark'
		`, teamID).Scan(&markNameCount).Error
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), markNameCount, "same-name entities must coexist until evidence resolves identity")

	firstValue, err := repo.UpsertValue(ctx, V2UpsertValueInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		ValueType:      "date",
		CanonicalValue: "2026-07-17",
		Display:        "July 17, 2026",
	})
	require.NoError(t, err)
	require.False(t, firstValue.Existing)

	secondValue, err := repo.UpsertValue(ctx, V2UpsertValueInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		ValueType:      "date",
		CanonicalValue: "2026-07-17",
		Display:        "2026-07-17",
	})
	require.NoError(t, err)
	assert.True(t, secondValue.Existing)
	assert.Equal(t, firstValue.ValueID, secondValue.ValueID)
}

func TestV2SemanticValueUpsertConcurrentDuplicateReturnsCanonical(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "semantic-value-race-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "owner")
	repo := NewV2SemanticRepository(appDB, rls)

	const workers = 8
	start := make(chan struct{})
	results := make(chan *V2ValueRecord, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			value, err := repo.UpsertValue(ctx, V2UpsertValueInput{
				TeamID:         teamID,
				OwnerProfileID: ownerID,
				ValueType:      "string",
				CanonicalValue: "PostgreSQL",
				Display:        "PostgreSQL",
			})
			if err != nil {
				errs <- err
				return
			}
			results <- value
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(results)

	for err := range errs {
		require.NoError(t, err)
	}
	var firstID string
	for value := range results {
		require.NotNil(t, value)
		require.NotEmpty(t, value.ValueID)
		if firstID == "" {
			firstID = value.ValueID
			continue
		}
		assert.Equal(t, firstID, value.ValueID)
	}
	require.NotEmpty(t, firstID)
}

func TestV2SemanticRelationshipLifecycleAndRLS(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createV2LedgerTeam(t, adminDB, rls, "team-a")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamA, "owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamA, "owner-b")
	teamC := createV2LedgerTeam(t, adminDB, rls, "team-c")
	ownerC := createV2LedgerProfile(t, adminDB, rls, teamC, "owner-c")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	mark := createV2SemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "person", "Mark Huang")
	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "product", "PostgreSQL")

	firstIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"mark works on dense mem", "Mark Huang works on Dense-Mem.")
	first := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     firstIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:first",
			SpanStart:      0,
			SpanEnd:        len("Mark Huang works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, first.Relationship)
	assert.Equal(t, "validated_claim", first.Relationship.Tier)
	assert.Equal(t, "active", first.Relationship.Status)
	assert.Equal(t, 1, first.Relationship.SupportCount)

	secondIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"mark works on dense mem again", "The maintainer Mark Huang works on Dense-Mem.")
	second := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        secondIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     secondIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:second",
			SpanStart:      0,
			SpanEnd:        len("The maintainer Mark Huang works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, second.Relationship)
	assert.Equal(t, first.Relationship.RelationshipID, second.Relationship.RelationshipID)
	assert.Equal(t, 2, second.Relationship.SupportCount)
	assert.Equal(t, 2, second.Relationship.SourceGroupCount)

	edges, err := semanticRepo.ListSemanticEdges(ctx, teamA, 20)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, first.Relationship.RelationshipID, edges[0].RelationshipID)

	assertSameTeamCanReadSemanticEdge(t, ctx, appDB, rls, teamA, ownerB, first.Relationship.RelationshipID)
	assertCrossTeamCannotReadSemanticEdge(t, ctx, appDB, rls, teamA, teamC, ownerC)

	err = semanticRepo.RetractRelationship(ctx, V2RetractRelationshipInput{
		TeamID:         teamA,
		OwnerProfileID: ownerB,
		RelationshipID: first.Relationship.RelationshipID,
		Reason:         "wrong owner",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2SemanticOwnerMismatch), err)

	candidateIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"dense mem may use postgres", "Dense-Mem may use PostgreSQL.")
	candidate := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        candidateIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		EvidenceVerdict: "insufficient",
	})
	require.NotNil(t, candidate.Relationship)
	assert.Equal(t, "candidate", candidate.Relationship.Tier)
	assert.Equal(t, "pending_evidence", candidate.Relationship.Status)

	hypothesisID, err := semanticRepo.CreateHypothesis(ctx, V2CreateHypothesisInput{
		TeamID:         teamA,
		OwnerProfileID: ownerA,
		Payload: map[string]any{
			"text": "Dense-Mem might use PostgreSQL.",
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, hypothesisID)

	edges, err = semanticRepo.ListSemanticEdges(ctx, teamA, 20)
	require.NoError(t, err)
	require.Len(t, edges, 1, "candidates and hypotheses must not become SemanticEdges")

	unknown := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        candidateIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "dogfoods",
		ObjectEntityID:  postgres.EntityID,
		EvidenceVerdict: "entailed",
		Support: &V2EvidenceSupportInput{
			FragmentID:     candidateIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:unknown",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem may use PostgreSQL."),
			Authority:      "primary",
		},
	})
	assert.Nil(t, unknown.Relationship)
	assert.NotEmpty(t, unknown.ReviewTaskID)

	ownerBIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerB,
		"owner b correction", "Profile B says Mark Huang works on Dense-Mem.")
	ownerBRelationship := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerB,
		IngestID:        ownerBIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     ownerBIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:owner-b",
			SpanStart:      0,
			SpanEnd:        len("Profile B says Mark Huang works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, ownerBRelationship.Relationship)
	crossRefID, err := semanticRepo.AppendCrossReference(ctx, V2AppendCrossReferenceInput{
		TeamID:                    teamA,
		AuthorProfileID:           ownerB,
		SourceRelationshipID:      ownerBRelationship.Relationship.RelationshipID,
		SourceRelationshipVersion: ownerBRelationship.Relationship.Version,
		TargetRelationshipID:      first.Relationship.RelationshipID,
		TargetRelationshipVersion: second.Relationship.Version,
		Kind:                      "challenges",
		VerificationEventID:       ownerBRelationship.VerificationEventID,
	})
	require.NoError(t, err)
	require.NotEmpty(t, crossRefID)

	var ownerAStatus string
	err = rls.WithTeamProfileTx(ctx, appDB, teamA, ownerB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamA, first.Relationship.RelationshipID).Scan(&ownerAStatus).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "active", ownerAStatus, "cross-profile references must not mutate the target owner")

	_, err = semanticRepo.AppendCrossReference(ctx, V2AppendCrossReferenceInput{
		TeamID:                    teamA,
		AuthorProfileID:           ownerB,
		SourceRelationshipID:      ownerBRelationship.Relationship.RelationshipID,
		SourceRelationshipVersion: ownerBRelationship.Relationship.Version + 1,
		TargetRelationshipID:      first.Relationship.RelationshipID,
		TargetRelationshipVersion: second.Relationship.Version,
		Kind:                      "challenges",
		VerificationEventID:       ownerBRelationship.VerificationEventID,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "relationship version does not match")

	_, err = semanticRepo.ApplyRelationshipDecision(ctx, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerB,
		IngestID:        ownerBIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     firstIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:cross-owner",
			SpanStart:      0,
			SpanEnd:        len("Mark Huang works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2SemanticOwnerMismatch), err)
}

func TestV2SemanticOneCardinalitySupersedesPriorActiveRelationship(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "one-cardinality-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	neo4j := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "Neo4j")
	firstIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"primary database postgres", "Dense-Mem uses PostgreSQL as its primary database.")
	first := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "primary_database",
		ObjectEntityID:  postgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     firstIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:primary-db-1",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses PostgreSQL as its primary database."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "active", first.Relationship.Status)

	secondIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"primary database neo4j", "Dense-Mem used Neo4j as its primary database before V2.")
	second := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        secondIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "primary_database",
		ObjectEntityID:  neo4j.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     secondIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:primary-db-2",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem used Neo4j as its primary database before V2."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "active", second.Relationship.Status)
	require.NotEqual(t, first.Relationship.RelationshipID, second.Relationship.RelationshipID)

	var firstStatus string
	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, first.Relationship.RelationshipID).Scan(&firstStatus).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "superseded", firstStatus)

	edges, err := semanticRepo.ListSemanticEdges(ctx, teamID, 20)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, second.Relationship.RelationshipID, edges[0].RelationshipID)
}

func TestV2SemanticAppendOnlyHistoryAndRetraction(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "append-only-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	subject := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Alex")
	object := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	ingest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"alex works on dense mem", "Alex works on Dense-Mem.")
	decision := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  object.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     ingest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:append-only",
			SpanStart:      0,
			SpanEnd:        len("Alex works on Dense-Mem."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, decision.Relationship)

	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE relationship_transition_events
			SET reason = 'rewritten'
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, decision.Relationship.RelationshipID)
		require.NoError(t, result.Error)
		assert.Equal(t, int64(0), result.RowsAffected)
		return nil
	})
	require.NoError(t, err)

	err = rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_transition_events
			SET reason = 'rewritten'
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, decision.Relationship.RelationshipID).Error
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append-only")

	require.NoError(t, semanticRepo.RetractRelationship(ctx, V2RetractRelationshipInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		RelationshipID: decision.Relationship.RelationshipID,
		Reason:         "forget",
	}))
	edges, err := semanticRepo.ListSemanticEdges(ctx, teamID, 20)
	require.NoError(t, err)
	assert.Empty(t, edges, "retracted relationships must disappear from SemanticEdge reads")

	var transitionCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_transition_events
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, decision.Relationship.RelationshipID).Scan(&transitionCount).Error
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, transitionCount, int64(2))
}

func createV2SemanticEntity(
	t *testing.T,
	ctx context.Context,
	repo *V2SemanticRepositoryImpl,
	teamID string,
	ownerID string,
	kind string,
	name string,
) *V2EntityRecord {
	t.Helper()
	entity, err := repo.CreateEntity(ctx, V2CreateEntityInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EntityKind:     kind,
		CanonicalName:  name,
	})
	require.NoError(t, err)
	return entity
}

func createV2SemanticIngest(
	t *testing.T,
	ctx context.Context,
	repo *V2LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	idempotencyKey string,
	content string,
) *V2CreateIngestResult {
	t.Helper()
	result, err := repo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: idempotencyKey,
		RequestHash:    sha256Hex(content),
		Evidence: []V2EvidenceInput{{
			Content: content,
		}},
	})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 1)
	return result
}

func applyV2SemanticDecision(
	t *testing.T,
	ctx context.Context,
	repo *V2SemanticRepositoryImpl,
	input V2ApplyRelationshipDecisionInput,
) *V2RelationshipDecisionResult {
	t.Helper()
	result, err := repo.ApplyRelationshipDecision(ctx, input)
	require.NoError(t, err)
	return result
}

func assertSameTeamCanReadSemanticEdge(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID string,
	profileID string,
	relationshipID string,
) {
	t.Helper()
	var count int64
	err := rls.WithTeamProfileTx(ctx, db, teamID, profileID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM semantic_edges
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, relationshipID).Scan(&count).Error
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func assertCrossTeamCannotReadSemanticEdge(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	targetTeamID string,
	readerTeamID string,
	readerProfileID string,
) {
	t.Helper()
	var count int64
	err := rls.WithTeamProfileTx(ctx, db, readerTeamID, readerProfileID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM semantic_edges
			WHERE team_id = ?::uuid
		`, targetTeamID).Scan(&count).Error
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}
