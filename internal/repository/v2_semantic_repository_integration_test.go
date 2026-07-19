package repository

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
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

func TestV2SemanticApplyRelationshipDecisionValidationRejectsMalformedSupportForAnyVerdict(t *testing.T) {
	input := validV2ApplyRelationshipDecisionInput()
	input.EvidenceVerdict = "insufficient"
	input.Support = &V2EvidenceSupportInput{
		FragmentID:     uuid.NewString(),
		SourceID:       uuid.NewString(),
		SourceGroupKey: "conversation:1",
		SpanStart:      0,
		SpanEnd:        5,
		Authority:      "primary",
	}

	err := validateV2ApplyRelationshipDecisionInput(normalizeV2ApplyRelationshipDecisionInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "source_id and source_revision_id must be provided together")
}

func TestV2SemanticApplyRelationshipDecisionValidationRejectsLegacySupportAuthority(t *testing.T) {
	input := validV2ApplyRelationshipDecisionInput()
	input.Support.Authority = "derived"

	err := validateV2ApplyRelationshipDecisionInput(normalizeV2ApplyRelationshipDecisionInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported support authority")
}

func TestV2SemanticApplyRelationshipDecisionValidationRequiresEntailedSupport(t *testing.T) {
	input := validV2ApplyRelationshipDecisionInput()
	input.Support = nil

	err := validateV2ApplyRelationshipDecisionInput(normalizeV2ApplyRelationshipDecisionInput(input))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "entailed relationship decisions require support")
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

func validV2ApplyRelationshipDecisionInput() V2ApplyRelationshipDecisionInput {
	return V2ApplyRelationshipDecisionInput{
		TeamID:           uuid.NewString(),
		OwnerProfileID:   uuid.NewString(),
		IngestID:         uuid.NewString(),
		SubjectEntityID:  uuid.NewString(),
		PredicateKey:     "works_on",
		PredicateVersion: 1,
		ObjectEntityID:   uuid.NewString(),
		EvidenceVerdict:  "entailed",
		Support: &V2EvidenceSupportInput{
			FragmentID:     uuid.NewString(),
			SourceGroupKey: "conversation:1",
			SpanStart:      0,
			SpanEnd:        5,
			Authority:      "primary",
		},
	}
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

	var beforeObservations, beforeReviews int64
	err = rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
				SELECT COUNT(*)
				FROM relationship_observations
				WHERE team_id = ?::uuid
			`, teamA).Scan(&beforeObservations).Error; err != nil {
			return err
		}
		return tx.Raw(`
				SELECT COUNT(*)
				FROM review_tasks
				WHERE team_id = ?::uuid
			`, teamA).Scan(&beforeReviews).Error
	})
	require.NoError(t, err)
	_, err = semanticRepo.ApplyRelationshipDecision(ctx, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerB,
		IngestID:        candidateIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "unknown_cross_owner_predicate",
		ObjectEntityID:  postgres.EntityID,
		EvidenceVerdict: "entailed",
		Support: &V2EvidenceSupportInput{
			FragmentID:     candidateIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:cross-owner-unknown",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem may use PostgreSQL."),
			Authority:      "primary",
		},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2SemanticOwnerMismatch), err)
	err = rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		var afterObservations, afterReviews int64
		if err := tx.Raw(`
				SELECT COUNT(*)
				FROM relationship_observations
				WHERE team_id = ?::uuid
			`, teamA).Scan(&afterObservations).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
				SELECT COUNT(*)
				FROM review_tasks
				WHERE team_id = ?::uuid
			`, teamA).Scan(&afterReviews).Error; err != nil {
			return err
		}
		assert.Equal(t, beforeObservations, afterObservations)
		assert.Equal(t, beforeReviews, afterReviews)
		return nil
	})
	require.NoError(t, err)

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

	ownerBOtherIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerB,
		"owner b postgres usage", "Profile B says Dense-Mem uses PostgreSQL.")
	ownerBUnrelated := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerB,
		IngestID:        ownerBOtherIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     ownerBOtherIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:owner-b-unrelated",
			SpanStart:      0,
			SpanEnd:        len("Profile B says Dense-Mem uses PostgreSQL."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, ownerBUnrelated.Relationship)
	_, err = semanticRepo.AppendCrossReference(ctx, V2AppendCrossReferenceInput{
		TeamID:                    teamA,
		AuthorProfileID:           ownerB,
		SourceRelationshipID:      ownerBRelationship.Relationship.RelationshipID,
		SourceRelationshipVersion: ownerBRelationship.Relationship.Version,
		TargetRelationshipID:      first.Relationship.RelationshipID,
		TargetRelationshipVersion: second.Relationship.Version,
		Kind:                      "challenges",
		VerificationEventID:       ownerBUnrelated.VerificationEventID,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verification event does not match source relationship")

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

func TestV2SemanticSupportSourceRevisionMustMatchSource(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "semantic-support-source-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	sourceOne, err := ledgerRepo.AdvanceSourceRevision(ctx, V2AdvanceSourceRevisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKey:      "doc://one",
		RevisionToken:  "rev-1",
		ContentHash:    "sha256:one",
	})
	require.NoError(t, err)
	sourceTwo, err := ledgerRepo.AdvanceSourceRevision(ctx, V2AdvanceSourceRevisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKey:      "doc://two",
		RevisionToken:  "rev-1",
		ContentHash:    "sha256:two",
	})
	require.NoError(t, err)

	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	ingest, err := ledgerRepo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []V2EvidenceInput{{
			Content:                   "Dense-Mem uses PostgreSQL.",
			SourceKey:                 "doc://one",
			SourceRevisionToken:       "rev-1",
			SourceRevisionContentHash: "sha256:one",
		}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Evidence, 1)

	_, err = semanticRepo.ApplyRelationshipDecision(ctx, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:       ingest.Evidence[0].FragmentID,
			SourceID:         sourceOne.SourceID,
			SourceRevisionID: sourceTwo.SourceRevisionID,
			SourceGroupKey:   "doc://one",
			SpanStart:        0,
			SpanEnd:          len("Dense-Mem uses PostgreSQL."),
			Authority:        "primary",
		},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2SemanticOwnerMismatch), err)

	matching := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        ingest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:       ingest.Evidence[0].FragmentID,
			SourceID:         sourceOne.SourceID,
			SourceRevisionID: sourceOne.SourceRevisionID,
			SourceGroupKey:   "doc://one",
			SpanStart:        0,
			SpanEnd:          len("Dense-Mem uses PostgreSQL."),
			Authority:        "primary",
		},
	})
	require.NotNil(t, matching.Relationship)
	assert.NotEmpty(t, matching.SupportID)
}

func TestV2SemanticCreateHypothesisBootstrapsRefsAndDefaultsProposed(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "hypothesis-bootstrap-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "owner")
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	hypothesisID, err := semanticRepo.CreateHypothesis(ctx, V2CreateHypothesisInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Payload:        map[string]any{"statement": "Dense-Mem may use PostgreSQL."},
	})
	require.NoError(t, err)
	require.NotEmpty(t, hypothesisID)

	err = rls.WithMigrationTx(ctx, adminDB, func(tx *gorm.DB) error {
		var status string
		if err := tx.Raw(`
			SELECT status
			FROM hypotheses
			WHERE team_id = ?::uuid
			  AND hypothesis_id = ?::uuid
		`, teamID, hypothesisID).Scan(&status).Error; err != nil {
			return err
		}
		assert.Equal(t, "proposed", status)

		var refCount int64
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM semantic_profile_refs
			WHERE team_id = ?::uuid
			  AND profile_id = ?::uuid
		`, teamID, ownerID).Scan(&refCount).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), refCount)
		return nil
	})
	require.NoError(t, err)
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

	thirdIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"primary database postgres again", "Dense-Mem now uses PostgreSQL as its primary database.")
	third := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        thirdIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "primary_database",
		ObjectEntityID:  postgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     thirdIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:primary-db-3",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem now uses PostgreSQL as its primary database."),
			Authority:      "primary",
		},
	})
	require.Equal(t, first.Relationship.RelationshipID, third.Relationship.RelationshipID)
	assert.Equal(t, "active", third.Relationship.Status)

	var reopenedStatus string
	var reopenedRecordedTo sql.NullTime
	var supersededStatus string
	var supersededRecordedTo sql.NullTime
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status, recorded_to
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, third.Relationship.RelationshipID).Row().Scan(&reopenedStatus, &reopenedRecordedTo); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status, recorded_to
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, second.Relationship.RelationshipID).Row().Scan(&supersededStatus, &supersededRecordedTo)
	})
	require.NoError(t, err)
	assert.Equal(t, "active", reopenedStatus)
	assert.False(t, reopenedRecordedTo.Valid)
	assert.Equal(t, "superseded", supersededStatus)
	assert.True(t, supersededRecordedTo.Valid)

	edges, err = semanticRepo.ListSemanticEdges(ctx, teamID, 20)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, third.Relationship.RelationshipID, edges[0].RelationshipID)
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

func TestV2SemanticTraceRelationshipHydratesLineageAndBoundedGraph(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createV2LedgerTeam(t, adminDB, rls, "trace-team-a")
	ownerA := createV2LedgerProfile(t, adminDB, rls, teamA, "trace-owner-a")
	ownerB := createV2LedgerProfile(t, adminDB, rls, teamA, "trace-owner-b")
	teamB := createV2LedgerTeam(t, adminDB, rls, "trace-team-b")
	createV2LedgerProfile(t, adminDB, rls, teamB, "trace-owner-other")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)
	searchRepo := NewV2SearchRepository(appDB, rls)

	mark := createV2SemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "person", "Mark Huang")
	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamA, ownerA, "product", "PostgreSQL")
	releaseDate, err := semanticRepo.UpsertValue(ctx, V2UpsertValueInput{
		TeamID:         teamA,
		OwnerProfileID: ownerA,
		ValueType:      "date",
		CanonicalValue: "2026-07-17",
		Display:        "July 17, 2026",
	})
	require.NoError(t, err)

	firstIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"trace mark works on dense mem", "Mark Huang works on Dense-Mem.")
	first := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     firstIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:trace:first",
			SpanStart:      0,
			SpanEnd:        len("Mark Huang works on Dense-Mem."),
			Quote:          "Mark Huang works on Dense-Mem.",
			Authority:      "primary",
		},
	})
	require.NotNil(t, first.Relationship)

	secondIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"trace dense mem release", "Dense-Mem released on July 17, 2026.")
	released := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerA,
		IngestID:        secondIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "released",
		ObjectValueID:   releaseDate.ValueID,
		PromoteToFact:   true,
		Support: &V2EvidenceSupportInput{
			FragmentID:     secondIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:trace:release",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem released on July 17, 2026."),
			Quote:          "Dense-Mem released on July 17, 2026.",
			Authority:      "primary",
		},
	})
	require.NotNil(t, released.Relationship)

	candidateIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerA,
		"trace candidate uses postgres", "Dense-Mem might use PostgreSQL.")
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

	ownerBIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamA, ownerB,
		"trace owner b challenge", "Profile B challenges Mark Huang works on Dense-Mem.")
	ownerBRelationship := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerB,
		IngestID:        ownerBIngest.IngestID,
		SubjectEntityID: mark.EntityID,
		PredicateKey:    "works_on",
		ObjectEntityID:  denseMem.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     ownerBIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:trace:owner-b",
			SpanStart:      0,
			SpanEnd:        len("Profile B challenges Mark Huang works on Dense-Mem."),
			Quote:          "Profile B challenges Mark Huang works on Dense-Mem.",
			Authority:      "primary",
		},
	})
	_, err = semanticRepo.AppendCrossReference(ctx, V2AppendCrossReferenceInput{
		TeamID:                    teamA,
		AuthorProfileID:           ownerB,
		SourceRelationshipID:      ownerBRelationship.Relationship.RelationshipID,
		SourceRelationshipVersion: ownerBRelationship.Relationship.Version,
		TargetRelationshipID:      first.Relationship.RelationshipID,
		TargetRelationshipVersion: first.Relationship.Version,
		Kind:                      "challenges",
		VerificationEventID:       ownerBRelationship.VerificationEventID,
	})
	require.NoError(t, err)

	_, err = searchRepo.UpsertSearchDocument(ctx, V2UpsertSearchDocumentInput{
		TeamID:         teamA,
		OwnerProfileID: ownerA,
		SourceKind:     "relationship",
		SourceID:       first.Relationship.RelationshipID,
		SourceVersion:  int64(first.Relationship.Version),
		DocumentText:   "Mark Huang works on Dense-Mem.",
		DocumentHash:   "sha256:trace-relationship",
	})
	require.NoError(t, err)

	err = rls.WithTeamProfileTx(ctx, appDB, teamA, ownerA, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO entity_correction_events (
			    team_id, owner_profile_id, action, survivor_entity_id,
			    selected_observation_ids, reason
			) VALUES (
			    ?::uuid, ?::uuid, 'merge', ?::uuid,
			    ARRAY[?::uuid], 'trace regression coverage'
			)
		`, teamA, ownerA, mark.EntityID, first.ObservationID).Error
	})
	require.NoError(t, err)

	trace, err := semanticRepo.TraceRelationship(ctx, V2TraceRelationshipInput{
		TeamID:                  teamA,
		RelationshipID:          first.Relationship.RelationshipID,
		MaxEdges:                1,
		MaxEvents:               20,
		MaxFragmentContentRunes: 12,
	})
	require.NoError(t, err)
	require.NotNil(t, trace.Relationship)
	assert.Equal(t, first.Relationship.RelationshipID, trace.Relationship.RelationshipID)
	assert.Equal(t, "Mark Huang", trace.Relationship.SubjectName)
	assert.Equal(t, "Dense-Mem", trace.Relationship.ObjectEntityName)
	require.NotEmpty(t, trace.Observations)
	require.NotEmpty(t, trace.EvidenceSupports)
	require.NotEmpty(t, trace.SupportDecisionEvents)
	require.NotEmpty(t, trace.VerificationEvents)
	require.NotEmpty(t, trace.Transitions)
	require.NotEmpty(t, trace.CrossProfileReferences)
	require.NotEmpty(t, trace.IdentityCorrections)
	require.NotEmpty(t, trace.SearchDocuments)
	require.NotEmpty(t, trace.EmbeddingJobs)
	require.NotEmpty(t, trace.EvidenceFragments)
	assert.True(t, trace.EvidenceFragments[0].ContentTruncated)
	assert.Equal(t, "Mark Huang w", trace.EvidenceFragments[0].Content)
	assert.Len(t, trace.SemanticEdges, 1)
	assert.True(t, trace.Truncated)
	assert.Equal(t, "max_edges", trace.StoppedReason)
	assert.Contains(t, trace.VisitedEntityIDs, mark.EntityID)
	assert.NotContains(t, trace.VisitedEntityIDs, postgres.EntityID, "candidate relationship endpoints must not appear in active graph context")

	noContent := false
	trace, err = semanticRepo.TraceRelationship(ctx, V2TraceRelationshipInput{
		TeamID:                 teamA,
		RelationshipID:         first.Relationship.RelationshipID,
		IncludeEvidenceContent: &noContent,
	})
	require.NoError(t, err)
	require.NotEmpty(t, trace.EvidenceFragments)
	assert.Empty(t, trace.EvidenceFragments[0].Content)

	_, err = semanticRepo.TraceRelationship(ctx, V2TraceRelationshipInput{
		TeamID:         teamB,
		RelationshipID: first.Relationship.RelationshipID,
	})
	require.Error(t, err)
}

func TestV2SemanticGraphReadsEntityValueEdgesAndNodeDetail(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "graph-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "graph-owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	releaseDate, err := semanticRepo.UpsertValue(ctx, V2UpsertValueInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		ValueType:      "date",
		CanonicalValue: "2026-07-17",
		Display:        "July 17, 2026",
	})
	require.NoError(t, err)

	usesIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"graph dense mem uses postgres", "Dense-Mem uses PostgreSQL.")
	uses := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        usesIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     usesIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:graph:uses",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses PostgreSQL."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, uses.Relationship)

	releaseIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"graph dense mem released", "Dense-Mem released on July 17, 2026.")
	released := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        releaseIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "released",
		ObjectValueID:   releaseDate.ValueID,
		PromoteToFact:   true,
		Support: &V2EvidenceSupportInput{
			FragmentID:     releaseIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:graph:released",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem released on July 17, 2026."),
			Authority:      "primary",
		},
	})
	require.NotNil(t, released.Relationship)

	graph, err := semanticRepo.SemanticGraph(ctx, V2SemanticGraphQuery{
		TeamID: teamID,
		Query:  "dense-mem",
		Limit:  20,
	})
	require.NoError(t, err)
	assert.Len(t, graph.Edges, 2)
	assertV2GraphHasNode(t, graph.Nodes, "entity", denseMem.EntityID, "Dense-Mem")
	assertV2GraphHasNode(t, graph.Nodes, "entity", postgres.EntityID, "PostgreSQL")
	assertV2GraphHasNode(t, graph.Nodes, "value", releaseDate.ValueID, "July 17, 2026")

	valueOnly, err := semanticRepo.SemanticGraph(ctx, V2SemanticGraphQuery{
		TeamID: teamID,
		Types:  []string{"value"},
		Limit:  20,
	})
	require.NoError(t, err)
	assert.Empty(t, valueOnly.Edges, "graph edges require Entity source nodes even when value nodes exist")

	local, err := semanticRepo.SemanticGraph(ctx, V2SemanticGraphQuery{
		TeamID:     teamID,
		Scope:      "local",
		AnchorType: "entity",
		AnchorID:   denseMem.EntityID,
		Depth:      1,
		Limit:      1,
	})
	require.NoError(t, err)
	assert.Len(t, local.Edges, 1)
	assert.True(t, local.Truncated)

	node, err := semanticRepo.SemanticGraphNodeDetail(ctx, V2SemanticGraphNodeDetailInput{
		TeamID:   teamID,
		NodeType: "entity",
		NodeID:   denseMem.EntityID,
	})
	require.NoError(t, err)
	assert.Equal(t, "Dense-Mem", node.Title)
	assert.Equal(t, "project", node.Body)

	valueNode, err := semanticRepo.SemanticGraphNodeDetail(ctx, V2SemanticGraphNodeDetailInput{
		TeamID:   teamID,
		NodeType: "value",
		NodeID:   releaseDate.ValueID,
	})
	require.NoError(t, err)
	assert.Equal(t, "July 17, 2026", valueNode.Title)
}

func assertV2GraphHasNode(t *testing.T, nodes []V2SemanticGraphNode, nodeType, id, title string) {
	t.Helper()
	for _, node := range nodes {
		if node.Type == nodeType && node.ID == id {
			assert.Equal(t, title, node.Title)
			return
		}
	}
	t.Fatalf("missing %s node %s in %+v", nodeType, id, nodes)
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
