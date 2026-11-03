//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestSemanticSupportMustMatchSuppliedIngest(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "semantic-support-ingest-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	semanticRepo := NewStore(appDB, rls, ConflictRuntimeConfig{})

	sourceOne, err := ledgerRepo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKey:      "doc://ingest-one",
		RevisionToken:  "rev-1",
		ContentHash:    "sha256:one",
	})
	require.NoError(t, err)
	sourceTwo, err := ledgerRepo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKey:      "doc://ingest-two",
		RevisionToken:  "rev-1",
		ContentHash:    "sha256:two",
	})
	require.NoError(t, err)

	firstIngest := createSemanticSourceIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"Dense-Mem uses PostgreSQL.", "doc://ingest-one", "sha256:one")
	secondIngest := createSemanticSourceIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"Dense-Mem used GraphDB.", "doc://ingest-two", "sha256:two")
	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")

	_, err = semanticRepo.ApplyRelationshipDecision(ctx, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: semanticSupportFromSource(
			secondIngest.Evidence[0].FragmentID, sourceTwo, "doc://ingest-two", "Dense-Mem used GraphDB.",
		),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSemanticOwnerMismatch), err)

	_, err = semanticRepo.ApplyRelationshipDecision(ctx, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: semanticSupportFromSource(
			firstIngest.Evidence[0].FragmentID, sourceTwo, "doc://ingest-two", "Dense-Mem uses PostgreSQL.",
		),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSemanticOwnerMismatch), err)

	matching := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: semanticSupportFromSource(
			firstIngest.Evidence[0].FragmentID, sourceOne, "doc://ingest-one", "Dense-Mem uses PostgreSQL.",
		),
	})
	require.NotNil(t, matching.Relationship)
	assert.NotEmpty(t, matching.SupportID)
}

func TestSemanticOneCardinalityUpgradeSupersedesLegacyManyRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "one-cardinality-upgrade-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	semanticRepo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	insertSearchTestContract(t, adminDB, rls, "cardinality-upgrade", 3, "exact", "")

	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "GraphDB")
	predicateKey := "sole_runtime_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	require.NoError(t, insertCardinalityUpgradePredicate(ctx, adminDB, rls, predicateKey))

	postgresIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "uses postgres", "Dense-Mem uses PostgreSQL.")
	postgresUse := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        postgresIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    predicateKey,
		ObjectEntityID:  postgres.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     postgresIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:uses-postgres",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses PostgreSQL."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", postgresUse.Relationship.CurrentCardinality)

	graphdbIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "uses graphdb", "Dense-Mem uses GraphDB.")
	graphdbUse := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        graphdbIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    predicateKey,
		ObjectEntityID:  graphdb.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     graphdbIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:uses-graphdb",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses GraphDB."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", graphdbUse.Relationship.CurrentCardinality)
	search := newSearchFixtureStore(appDB, rls)
	document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: graphdbUse.Relationship.RelationshipID,
		SourceVersion: int64(graphdbUse.Relationship.Version), ProjectionFormat: 2, DocumentText: "legacy many GraphDB",
		SpaceID: graphdbUse.Relationship.SpaceID, SpaceGeneration: graphdbUse.Relationship.SpaceGeneration,
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, search, teamID, map[string][]float32{document.SearchDocumentID: {1, 0, 0}})

	upgradeIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"uses postgres as sole database", "Dense-Mem uses PostgreSQL as its sole durable database.")
	upgraded := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         upgradeIngest.IngestID,
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     predicateKey,
		PredicateVersion: 2,
		ObjectEntityID:   postgres.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     upgradeIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:uses-postgres-sole",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses PostgreSQL as its sole durable database."),
			Authority:      "primary",
		},
	})
	require.Equal(t, postgresUse.Relationship.RelationshipID, upgraded.Relationship.RelationshipID)
	assert.Equal(t, "one", upgraded.Relationship.CurrentCardinality)

	var legacySiblingStatus string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, graphdbUse.Relationship.RelationshipID).Scan(&legacySiblingStatus).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "superseded", legacySiblingStatus)
	documents, err := search.LoadSearchDocumentsForSources(ctx, LoadSearchDocumentsForSourcesInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceIDs: []string{graphdbUse.Relationship.RelationshipID},
	})
	require.NoError(t, err)
	require.Len(t, documents, 1)
	require.Equal(t, "not_required", documents[0].SearchState)
	require.EqualValues(t, graphdbUse.Relationship.Version+1, documents[0].SourceVersion)

	edgeIDs, err := listSemanticEdgeIDsForTest(ctx, appDB, rls, teamID)
	require.NoError(t, err)
	require.Len(t, edgeIDs, 1)
	assert.Equal(t, upgraded.Relationship.RelationshipID, edgeIDs[0])
}

type semanticSearchDiagnosticConfig string

func (c semanticSearchDiagnosticConfig) GetPostgresDSN() string { return string(c) }

func TestSemanticOneCardinalitySearchRetirementIsAtomicAndIsolated(t *testing.T) {
	adminDB, fixtureDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := correlation.WithID(context.Background(), uuid.NewString())
	var logs bytes.Buffer
	logger := observability.NewWithHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	appDB, err := storagepostgres.OpenWithLogger(ctx, semanticSearchDiagnosticConfig(fixtureDB.Dialector.(*gormpostgres.Dialector).DSN), logger)
	require.NoError(t, err)
	connection, err := appDB.DB()
	require.NoError(t, err)
	defer connection.Close()
	contractID := insertSearchTestContract(t, adminDB, rls, "cardinality-search", 3, "exact", "")
	store := NewStore(appDB, rls, ConflictRuntimeConfig{})
	search := newSearchFixtureStore(appDB, rls)
	teamID := createLedgerTeam(t, adminDB, rls, "cardinality-search")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "c")
	scoped := func(team, owner string) context.Context {
		return requestctx.WithActor(ctx, requestctx.Actor{TeamID: uuid.MustParse(team), OwnerID: uuid.MustParse(owner)})
	}
	subject := createSemanticEntity(t, scoped(teamID, ownerA), store, teamID, ownerA, "project", "Cardinality project")
	postgres := createSemanticEntity(t, scoped(teamID, ownerA), store, teamID, ownerA, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, scoped(teamID, ownerA), store, teamID, ownerA, "product", "GraphDB")
	first := commitConflictRememberFixture(t, scoped(teamID, ownerA), store, teamID, ownerA, subject.EntityID, postgres.EntityID, "Project uses PostgreSQL.", "cardinality-first")
	firstID := first.RelationshipResults[0].Relationship.RelationshipID
	protectedB := commitConflictRememberFixture(t, scoped(teamID, ownerB), store, teamID, ownerB, subject.EntityID, postgres.EntityID, "B confirms PostgreSQL.", "cardinality-b")
	protectedC := commitConflictRememberFixture(t, scoped(teamID, ownerC), store, teamID, ownerC, subject.EntityID, postgres.EntityID, "C confirms PostgreSQL.", "cardinality-c")
	scopeInput := conflictRememberFixtureInput(teamID, ownerA, subject.EntityID, postgres.EntityID, "Analytics uses PostgreSQL.", "cardinality-scope", nil)
	scopeInput.Commit.RelationshipObservations[0].Observation.ScopeKey = "analytics"
	protectedScope, err := store.CommitRememberWithEmbeddings(scoped(teamID, ownerA), scopeInput, conflictRememberFixtureVectors(t, scoped(teamID, ownerA), store, scopeInput))
	require.NoError(t, err)
	otherTeam := createLedgerTeam(t, adminDB, rls, "cardinality-other")
	otherOwner := createLedgerProfile(t, adminDB, rls, otherTeam, "other")
	otherSubject := createSemanticEntity(t, scoped(otherTeam, otherOwner), store, otherTeam, otherOwner, "project", "Cardinality project")
	otherObject := createSemanticEntity(t, scoped(otherTeam, otherOwner), store, otherTeam, otherOwner, "product", "PostgreSQL")
	protectedTeam := commitConflictRememberFixture(t, scoped(otherTeam, otherOwner), store, otherTeam, otherOwner, otherSubject.EntityID, otherObject.EntityID, "Other team uses PostgreSQL.", "cardinality-team")
	type projection struct {
		Document SearchDocumentForEmbedding
		Version  int
		Status   string
		Vector   string
	}
	load := func(team, owner, id string) projection {
		t.Helper()
		documents, err := search.LoadSearchDocumentsForSources(scoped(team, owner), LoadSearchDocumentsForSourcesInput{TeamID: team, OwnerProfileID: owner, SourceKind: "relationship", SourceIDs: []string{id}})
		require.NoError(t, err)
		require.Len(t, documents, 1)
		value := projection{Document: documents[0]}
		require.NoError(t, rls.WithTeamProfileTx(scoped(team, owner), appDB, team, owner, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT relationship.version, relationship.status, COALESCE(document.embedding::text, '')
				FROM relationship_records relationship JOIN search_documents document
				ON document.team_id=relationship.team_id AND document.source_kind='relationship' AND document.source_id=relationship.relationship_id
				WHERE relationship.team_id=?::uuid AND relationship.relationship_id=?::uuid AND document.embedding_contract_id=?::uuid`, team, id, contractID).Row().Scan(&value.Version, &value.Status, &value.Vector)
		}))
		return value
	}
	before := load(teamID, ownerA, firstID)
	protected := []struct{ team, owner, id string }{
		{teamID, ownerB, protectedB.RelationshipResults[0].Relationship.RelationshipID},
		{teamID, ownerC, protectedC.RelationshipResults[0].Relationship.RelationshipID},
		{teamID, ownerA, protectedScope.RelationshipResults[0].Relationship.RelationshipID},
		{otherTeam, otherOwner, protectedTeam.RelationshipResults[0].Relationship.RelationshipID},
	}
	unchanged := make([]projection, len(protected))
	for i, item := range protected {
		unchanged[i] = load(item.team, item.owner, item.id)
	}
	replacement := conflictRememberFixtureInput(teamID, ownerA, subject.EntityID, graphdb.EntityID, "Project now uses GraphDB.", "cardinality-replacement", nil)
	plan, err := store.PlanRememberEmbeddings(scoped(teamID, ownerA), replacement)
	require.NoError(t, err)
	vectors := rememberTestEmbeddings(plan, false)
	invalid := append([]InlineEmbeddingResult(nil), vectors...)
	changed := false
	for i, document := range plan.Documents {
		if document.SourceKind == "relationship" {
			invalid[i].DocumentHash = sha256Hex("missing relationship embedding")
			changed = true
		}
	}
	require.True(t, changed)
	logs.Reset()
	_, err = store.CommitRememberWithEmbeddings(scoped(teamID, ownerA), replacement, invalid)
	require.Error(t, err)
	require.Equal(t, "embeddings", RememberCommitFailureStage(err))
	require.Contains(t, logs.String(), "search_state = 'not_required'")
	require.Contains(t, logs.String(), `"team_id":"`+teamID+`"`)
	require.Contains(t, logs.String(), `"profile_id":"`+ownerA+`"`)
	require.Contains(t, logs.String(), correlation.FromContext(ctx))
	require.Equal(t, before, load(teamID, ownerA, firstID), "late failure must roll back supersession and retirement")
	second, err := store.CommitRememberWithEmbeddings(scoped(teamID, ownerA), replacement, vectors)
	require.NoError(t, err)
	retired := load(teamID, ownerA, firstID)
	require.Equal(t, "superseded", retired.Status)
	require.Equal(t, "not_required", retired.Document.SearchState)
	require.Empty(t, retired.Vector)
	require.Empty(t, retired.Document.ProjectionGenerationID)
	require.EqualValues(t, retired.Version, retired.Document.SourceVersion)
	stale := SearchDocumentEmbedding{SearchDocumentID: before.Document.SearchDocumentID, SourceVersion: before.Document.SourceVersion,
		ProjectionFormat: before.Document.ProjectionFormat, ProjectionGenerationID: before.Document.ProjectionGenerationID,
		DocumentVersion: before.Document.DocumentVersion, DocumentHash: before.Document.DocumentHash,
		EmbeddingContractID: contractID, EmbeddingDimensions: 3, Embedding: []float32{0, 0, 1}, SpaceID: before.Document.SpaceID, SpaceGeneration: before.Document.SpaceGeneration}
	staleInput := CompleteSearchDocumentsWithEmbeddingsInput{TeamID: teamID, OwnerProfileID: ownerA, Documents: []SearchDocumentEmbedding{stale}}
	require.ErrorIs(t, search.CompleteSearchDocumentsWithEmbeddings(scoped(teamID, ownerA), staleInput), ErrSearchStaleVersion)
	_, err = store.CommitRememberWithEmbeddings(scoped(teamID, ownerA), replacement, vectors)
	require.ErrorIs(t, err, ErrRememberReplay)
	require.Equal(t, retired, load(teamID, ownerA, firstID))
	third := commitConflictRememberFixture(t, scoped(teamID, ownerA), store, teamID, ownerA, subject.EntityID, postgres.EntityID, "Project uses PostgreSQL again.", "cardinality-reactivation")
	require.Equal(t, firstID, third.RelationshipResults[0].Relationship.RelationshipID)
	current := load(teamID, ownerA, firstID)
	require.Equal(t, "active", current.Status)
	require.Equal(t, "current", current.Document.SearchState)
	require.Greater(t, current.Version, retired.Version)
	require.NoError(t, search.CompleteSearchDocumentsWithEmbeddings(scoped(teamID, ownerA), staleInput))
	require.Equal(t, current, load(teamID, ownerA, firstID), "older completion must not overwrite the reactivated vector")
	require.Equal(t, "not_required", load(teamID, ownerA, second.RelationshipResults[0].Relationship.RelationshipID).Document.SearchState)
	for i, item := range protected {
		require.Equal(t, unchanged[i], load(item.team, item.owner, item.id))
	}
	convergence, err := search.GetSearchConvergence(ctx, SearchConvergenceInput{EmbeddingContractID: contractID, EmbeddingDimensions: 3})
	require.NoError(t, err)
	require.Zero(t, convergence.DriftedDocuments)
	require.Equal(t, "converged", convergence.Status)
	t.Logf("cardinality search witness: retired version=%d; current version=%d; protected=%d; convergence=%s", retired.Version, current.Version, len(protected), convergence.Status)
	generationID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO search_projection_generations
			(team_id, projection_generation_id, source_kind, generation, projection_format_version, state,
			eligible_count, projected_count, current_vector_count, drifted_count, completed_at, activated_at)
			VALUES (?::uuid, ?::uuid, 'relationship', 1, 2, 'current', 1, 1, 1, 1, now(), now())`, teamID, generationID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE search_documents SET projection_generation_id=?::uuid
			WHERE team_id=?::uuid AND source_kind='relationship' AND source_id=?::uuid`, generationID, teamID, firstID).Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE embedding_contracts SET lifecycle_state='retired' WHERE embedding_contract_id=?::uuid", contractID).Error
	}))
	standaloneIngest := createSemanticIngest(t, scoped(teamID, ownerA), store, teamID, ownerA, "cardinality-no-active-contract", "Project selects GraphDB without an active search contract.")
	standalone := applySemanticDecision(t, scoped(teamID, ownerA), store, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerA, IngestID: standaloneIngest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "primary_database", ObjectEntityID: graphdb.EntityID,
		Support: &EvidenceSupportInput{FragmentID: standaloneIngest.Evidence[0].FragmentID, SourceGroupKey: "cardinality-no-contract", SpanEnd: len(standaloneIngest.Evidence[0].Content), Authority: "primary"},
	})
	require.Equal(t, "active", standalone.Relationship.Status)
	require.Equal(t, "not_required", load(teamID, ownerA, firstID).Document.SearchState)
	require.Empty(t, load(teamID, ownerA, firstID).Vector)
	require.Empty(t, load(teamID, ownerA, firstID).Document.ProjectionGenerationID)
	require.Contains(t, logs.String(), "UPDATE search_projection_generations SET drifted_count = 0")
	var drifted int64
	require.NoError(t, rls.WithTeamProfileTx(scoped(teamID, ownerA), appDB, teamID, ownerA, func(tx *gorm.DB) error {
		return tx.Raw("SELECT drifted_count FROM search_projection_generations WHERE team_id=?::uuid AND projection_generation_id=?::uuid", teamID, generationID).Row().Scan(&drifted)
	}))
	require.EqualValues(t, 1, drifted, "profile transactions must preserve system-owned generation counters")
}

func TestSemanticDelayedOlderOneVersionDoesNotSupersedeNewerManyRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "one-cardinality-reversal-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewStore(appDB, rls, ConflictRuntimeConfig{})
	semanticRepo := NewStore(appDB, rls, ConflictRuntimeConfig{})

	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "GraphDB")
	redis := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "Redis")
	predicateKey := "runtime_policy_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	require.NoError(t, insertPolicyReversalPredicate(ctx, adminDB, rls, predicateKey))

	postgresIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"policy reversal postgres", "Dense-Mem uses PostgreSQL as a durable store.")
	postgresUse := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         postgresIngest.IngestID,
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     predicateKey,
		PredicateVersion: 2,
		ObjectEntityID:   postgres.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     postgresIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:policy-reversal-postgres",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses PostgreSQL as a durable store."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", postgresUse.Relationship.CurrentCardinality)

	graphdbIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"policy reversal graphdb", "GraphDB remains legacy migration input.")
	graphdbUse := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         graphdbIngest.IngestID,
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     predicateKey,
		PredicateVersion: 2,
		ObjectEntityID:   graphdb.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     graphdbIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:policy-reversal-graphdb",
			SpanStart:      0,
			SpanEnd:        len("GraphDB remains legacy migration input."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", graphdbUse.Relationship.CurrentCardinality)

	delayedIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"delayed old redis", "Dense-Mem used Redis as its runtime memory store.")
	delayed := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        delayedIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    predicateKey,
		ObjectEntityID:  redis.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     delayedIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:delayed-old-redis",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem used Redis as its runtime memory store."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "one", delayed.Relationship.CurrentCardinality)

	statuses := loadRelationshipStatuses(t, ctx, appDB, rls, teamID, ownerID,
		postgresUse.Relationship.RelationshipID,
		graphdbUse.Relationship.RelationshipID,
		delayed.Relationship.RelationshipID,
	)
	assert.Equal(t, "active", statuses[postgresUse.Relationship.RelationshipID])
	assert.Equal(t, "active", statuses[graphdbUse.Relationship.RelationshipID])
	assert.Equal(t, "active", statuses[delayed.Relationship.RelationshipID])
}

func createSemanticSourceIngest(
	t *testing.T,
	ctx context.Context,
	repo *Store,
	teamID string,
	ownerID string,
	content string,
	sourceKey string,
	sourceRevisionContentHash string,
) *CreateIngestResult {
	t.Helper()
	result, err := repo.CreateIngestForTest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []EvidenceInput{{
			Content:                   content,
			SourceKey:                 sourceKey,
			SourceRevisionToken:       "rev-1",
			SourceRevisionContentHash: sourceRevisionContentHash,
		}},
	})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 1)
	return result
}

func semanticSupportFromSource(
	fragmentID string,
	source *SourceRevisionResult,
	sourceGroupKey string,
	content string,
) *EvidenceSupportInput {
	support := semanticSupport(sourceGroupKey, content)
	support.FragmentID = fragmentID
	support.SourceID = source.SourceID
	support.SourceRevisionID = source.SourceRevisionID
	return support
}

func semanticSupport(sourceGroupKey string, content string) *EvidenceSupportInput {
	return &EvidenceSupportInput{
		SourceGroupKey: sourceGroupKey,
		SpanStart:      0,
		SpanEnd:        len(content),
		Authority:      "primary",
	}
}

func loadRelationshipStatuses(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID string,
	ownerID string,
	relationshipIDs ...string,
) map[string]string {
	t.Helper()
	statuses := make(map[string]string, len(relationshipIDs))
	err := rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		for _, relationshipID := range relationshipIDs {
			var status string
			if err := tx.Raw(`
				SELECT status
				FROM relationship_records
				WHERE team_id = ?::uuid
				  AND relationship_id = ?::uuid
			`, teamID, relationshipID).Scan(&status).Error; err != nil {
				return err
			}
			statuses[relationshipID] = status
		}
		return nil
	})
	require.NoError(t, err)
	return statuses
}

func insertCardinalityUpgradePredicate(
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	predicateKey string,
) error {
	return rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO predicate_definitions (
			    predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
			    relationship_kind, current_cardinality
			) VALUES
			    (
			        ?, 1, ARRAY[]::text[],
			        ARRAY['project','product','organization','other']::text[],
			        ARRAY['project','product','concept','other']::text[],
			        'state', 'many'
			    ),
			    (
			        ?, 2, ARRAY['primary_runtime']::text[],
			        ARRAY['project','product','organization','other']::text[],
			        ARRAY['project','product','concept','other']::text[],
			        'state', 'one'
			    )
		`, predicateKey, predicateKey).Error
	})
}

func insertPolicyReversalPredicate(
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	predicateKey string,
) error {
	return rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO predicate_definitions (
			    predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
			    relationship_kind, current_cardinality
			) VALUES
			    (
			        ?, 1, ARRAY[]::text[],
			        ARRAY['project','product','organization','other']::text[],
			        ARRAY['project','product','concept','other']::text[],
			        'state', 'one'
			    ),
			    (
			        ?, 2, ARRAY['runtime_component']::text[],
			        ARRAY['project','product','organization','other']::text[],
			        ARRAY['project','product','concept','other']::text[],
			        'state', 'many'
			    )
		`, predicateKey, predicateKey).Error
	})
}
