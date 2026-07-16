package repository

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestSemanticRepositoryRealPostgresRetrieval(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("DENSE_MEM_SEMANTIC_TEST_DSN"))
	if dsn == "" {
		t.Skip("set DENSE_MEM_SEMANTIC_TEST_DSN to a disposable densemem_semantic_test database")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	var databaseName string
	require.NoError(t, db.Raw("SELECT current_database()").Scan(&databaseName).Error)
	require.True(t, strings.HasPrefix(databaseName, "densemem_semantic_test"), "integration test refuses to mutate a non-disposable database: %s", databaseName)
	migrator, err := storagepostgres.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(context.Background()))

	ctx := context.Background()
	teamID := uuid.NewString()
	profileID := uuid.NewString()
	rls := storagepostgres.NewRLS()
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec("INSERT INTO teams (id, name) VALUES (?, ?)", teamID, "Semantic repository integration "+teamID).Error
	}))
	repo := NewSemanticRepository(db, rls)
	remembered, err := repo.StoreRemember(ctx, SemanticRememberInput{
		TeamID:           teamID,
		TeamName:         "Semantic repository integration",
		OwnerProfileID:   profileID,
		OwnerProfileName: "Integration profile",
		Evidence: []SemanticEvidenceInput{{
			Content:        "Dense-Mem uses PostgreSQL. PostgreSQL stores semantic relationships.",
			SourceDocID:    "integration-doc",
			SourceGroup:    "integration",
			SourceType:     domain.SourceTypeDocument,
			Authority:      domain.AuthorityPrimary,
			IdempotencyKey: "integration:" + teamID,
		}},
		Relationships: []SemanticRelationshipInput{
			{
				SubjectName: "Dense-Mem", SubjectKind: domain.SemanticEntityProject,
				Predicate: "uses", Polarity: domain.PolarityPlus,
				ObjectName: "PostgreSQL", ObjectKind: domain.SemanticEntityProduct,
				Tier: domain.SemanticTierValidatedClaim, Status: domain.SemanticStatusActive,
				Confidence: 0.98, EvidenceIndex: 0, Quote: "Dense-Mem uses PostgreSQL.",
				EvidenceVerdict: "entailed", KnowledgeAlignment: "novel",
			},
			{
				SubjectName: "PostgreSQL", SubjectKind: domain.SemanticEntityProduct,
				Predicate: "stores", Polarity: domain.PolarityPlus,
				ObjectValue: "semantic relationships", ObjectKind: domain.SemanticEntityConcept,
				Tier: domain.SemanticTierValidatedClaim, Status: domain.SemanticStatusActive,
				Confidence: 0.97, EvidenceIndex: 0, Quote: "PostgreSQL stores semantic relationships.",
				EvidenceVerdict: "entailed", KnowledgeAlignment: "novel",
			},
			{
				SubjectName: "PostgreSQL", SubjectKind: domain.SemanticEntityProduct,
				Predicate: "is", Polarity: domain.PolarityPlus,
				ObjectValue: "a database", ObjectKind: domain.SemanticEntityConcept,
				Tier: domain.SemanticTierValidatedClaim, Status: domain.SemanticStatusActive,
				Confidence: 0.96, EvidenceIndex: 0, Quote: "PostgreSQL stores semantic relationships.",
				EvidenceVerdict: "entailed", KnowledgeAlignment: "novel",
			},
			{
				SubjectName: "Dense-Mem", SubjectKind: domain.SemanticEntityProject,
				Predicate: "might use", Polarity: domain.PolarityPlus,
				ObjectValue: "SQLite", ObjectKind: domain.SemanticEntityProduct,
				Tier: domain.SemanticTierCandidate, Status: domain.SemanticStatusNeedsReview,
				Confidence: 0.4, EvidenceIndex: 0, Quote: "Dense-Mem uses PostgreSQL.",
				EvidenceVerdict: "insufficient", KnowledgeAlignment: "ambiguous",
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, remembered.Relationships, 4)

	embedding := make([]float32, 3072)
	embedding[0] = 1
	processedJobs := 0
	for {
		jobs, err := repo.ClaimSemanticEmbeddingJobs(ctx, 64)
		require.NoError(t, err)
		if len(jobs) == 0 {
			break
		}
		for _, job := range jobs {
			require.NoError(t, repo.CompleteSemanticEmbeddingJob(ctx, job, embedding, "semantic-integration"))
		}
		processedJobs += len(jobs)
		require.Less(t, processedJobs, 20, "embedding queue should drain")
	}
	require.Greater(t, processedJobs, 0)

	scope := domain.SemanticRecallSearchScope{
		TeamID: teamID,
		Features: domain.SemanticRecallQueryFeatures{
			Query:        "What does Dense-Mem use for semantic relationships?",
			ContentQuery: "What does Dense-Mem use for semantic relationships?",
			RelaxedQuery: "dense OR mem OR use OR semantic OR relationships",
		},
		Embedding:                embedding,
		EmbeddingContractID:      "semantic-integration:3072:semantic_search_document_v1",
		ValidAt:                  time.Now().UTC(),
		KnownAt:                  time.Now().UTC(),
		BranchLimit:              60,
		RelationshipsPerEvidence: 2,
		KnownEvidenceIDs:         []string{},
		KnownRelationshipIDs:     []string{},
		ExpandFromEntityIDs:      []string{},
	}
	lexicalBatch, err := repo.SearchRecallLexicalCandidates(ctx, scope)
	require.NoError(t, err)
	require.NotEmpty(t, lexicalBatch.Candidates)
	require.NotEmpty(t, lexicalBatch.EntitySeeds)
	vectorBatch, err := repo.SearchRecallVectorCandidates(ctx, scope)
	require.NoError(t, err)
	require.NotEmpty(t, vectorBatch.Candidates)
	seeds := append(append([]domain.SemanticRecallEntitySeed{}, lexicalBatch.EntitySeeds...), vectorBatch.EntitySeeds...)
	adjacencyCandidates, err := repo.SearchRecallAdjacencyCandidates(ctx, scope, seeds)
	require.NoError(t, err)
	allCandidates := append(append(append([]domain.SemanticRecallCandidate{}, lexicalBatch.Candidates...), vectorBatch.Candidates...), adjacencyCandidates...)
	require.NotEmpty(t, allCandidates)
	evidenceIDs := make([]string, 0, len(allCandidates))
	relationshipIDs := make([]string, 0, len(allCandidates))
	for _, candidate := range allCandidates {
		evidenceIDs = append(evidenceIDs, candidate.EvidenceID)
		relationshipIDs = append(relationshipIDs, candidate.RelationshipIDs...)
	}
	results, err := repo.HydrateRecallEvidence(ctx, scope, evidenceIDs[:1], relationshipIDs)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.NotNil(t, results[0].Evidence)
	require.Equal(t, "integration-doc", results[0].Evidence.SourceDocID)
	require.NotEmpty(t, results[0].Relationships)
	for _, rel := range results[0].Relationships {
		require.Equal(t, domain.SemanticStatusActive, rel.Status)
		require.NotEqual(t, "might use", rel.Predicate)
	}

	var candidateCount int64
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*) FROM semantic_relationship_records
			WHERE team_id = ? AND tier = 'candidate' AND status = 'needs_review'
		`, teamID).Scan(&candidateCount).Error
	}))
	require.EqualValues(t, 1, candidateCount)
}

func TestValidateSemanticRememberInput(t *testing.T) {
	teamID := uuid.NewString()
	profileID := uuid.NewString()
	input := SemanticRememberInput{
		TeamID:         teamID,
		OwnerProfileID: profileID,
		Evidence: []SemanticEvidenceInput{{
			Content: "Dense-Mem uses Postgres for semantic relationships.",
		}},
		Relationships: []SemanticRelationshipInput{{
			SubjectName:        "Dense-Mem",
			SubjectKind:        domain.SemanticEntityProject,
			Predicate:          "uses",
			ObjectName:         "Postgres",
			ObjectKind:         domain.SemanticEntityProduct,
			Tier:               domain.SemanticTierValidatedClaim,
			Status:             domain.SemanticStatusActive,
			Polarity:           domain.PolarityPlus,
			Confidence:         0.8,
			EvidenceIndex:      0,
			EvidenceVerdict:    "entailed",
			KnowledgeAlignment: "novel",
		}},
	}

	normalized := normalizeSemanticRememberInput(input)
	if err := validateSemanticRememberInput(normalized); err != nil {
		t.Fatalf("validateSemanticRememberInput() error = %v", err)
	}
	if normalized.Evidence[0].SourceType != domain.SourceTypeConversation {
		t.Fatalf("default source type = %q, want conversation", normalized.Evidence[0].SourceType)
	}
	if normalized.Evidence[0].Authority != domain.AuthorityPrimary {
		t.Fatalf("default authority = %q, want primary", normalized.Evidence[0].Authority)
	}
	if normalized.Evidence[0].Labels == nil || len(normalized.Evidence[0].Labels) != 0 {
		t.Fatalf("default labels = %#v, want non-nil empty slice", normalized.Evidence[0].Labels)
	}
}

func TestValidateSemanticRememberInputRejectsInvalidRelationship(t *testing.T) {
	input := SemanticRememberInput{
		TeamID:         uuid.NewString(),
		OwnerProfileID: uuid.NewString(),
		Evidence: []SemanticEvidenceInput{{
			Content: "evidence",
		}},
		Relationships: []SemanticRelationshipInput{{
			SubjectName:   "subject",
			Predicate:     "uses",
			Tier:          domain.SemanticRelationshipTier("dream"),
			EvidenceIndex: 0,
		}},
	}

	err := validateSemanticRememberInput(normalizeSemanticRememberInput(input))
	if err == nil {
		t.Fatal("validateSemanticRememberInput() expected error")
	}
	if !strings.Contains(err.Error(), "must set exactly one object form") {
		t.Fatalf("error = %q, want missing object", err)
	}
}

func TestValidateSemanticRememberInputRejectsInvalidVerifierFields(t *testing.T) {
	input := SemanticRememberInput{
		TeamID:         uuid.NewString(),
		OwnerProfileID: uuid.NewString(),
		Evidence: []SemanticEvidenceInput{{
			Content: "evidence",
		}},
		Relationships: []SemanticRelationshipInput{{
			SubjectName:        "subject",
			SubjectKind:        domain.SemanticEntityConcept,
			Predicate:          "uses",
			Polarity:           domain.PolarityPlus,
			ObjectValue:        "object",
			ObjectKind:         domain.SemanticEntityConcept,
			Tier:               domain.SemanticTierValidatedClaim,
			Status:             domain.SemanticStatusActive,
			EvidenceIndex:      0,
			EvidenceVerdict:    "maybe",
			KnowledgeAlignment: "novel",
		}},
	}

	err := validateSemanticRememberInput(normalizeSemanticRememberInput(input))

	require.ErrorContains(t, err, "evidence_verdict is invalid")
}

func TestSemanticJSONPayloadRequiresValidJSON(t *testing.T) {
	payload, err := semanticJSONPayload("")
	require.NoError(t, err)
	require.Equal(t, "{}", payload)

	payload, err = semanticJSONPayload(`{"ok": true}`)
	require.NoError(t, err)
	require.Equal(t, `{"ok": true}`, payload)

	_, err = semanticJSONPayload(`{"ok":`)
	require.ErrorContains(t, err, "invalid json")
}

func TestSemanticContentHashNormalizesWhitespace(t *testing.T) {
	left := semanticContentHash("  Dense-Mem uses Postgres. ")
	right := semanticContentHash("Dense-Mem uses Postgres.")
	if left != right {
		t.Fatal("semanticContentHash should trim surrounding whitespace")
	}
}

func TestUpsertSemanticRelationshipClosesReturningRowsBeforeEventInsert(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	now := time.Date(2026, 7, 12, 16, 10, 0, 0, time.UTC)
	teamID := uuid.NewString()
	ownerProfileID := uuid.NewString()
	relationshipID := uuid.NewString()
	subjectEntityID := uuid.NewString()
	input := SemanticRememberInput{
		TeamID:         teamID,
		OwnerProfileID: ownerProfileID,
	}
	item := SemanticRelationshipInput{
		Predicate:   "uses",
		Polarity:    domain.PolarityPlus,
		ObjectValue: "Postgres",
		ObjectKind:  domain.SemanticEntityProduct,
		Tier:        domain.SemanticTierValidatedClaim,
		Status:      domain.SemanticStatusActive,
		Confidence:  0.95,
	}
	relationshipRows := sqlmock.NewRows([]string{
		"team_id",
		"relationship_id",
		"owner_profile_id",
		"subject_entity_id",
		"predicate",
		"polarity",
		"object_entity_id",
		"object_value",
		"object_kind",
		"tier",
		"status",
		"confidence",
		"support_count",
		"source_group_count",
		"semantic_group_key",
		"version",
		"valid_from",
		"valid_to",
		"recorded_at",
		"recorded_to",
		"created_at",
		"updated_at",
	}).AddRow(
		teamID,
		relationshipID,
		ownerProfileID,
		subjectEntityID,
		"uses",
		string(domain.PolarityPlus),
		"",
		"Postgres",
		string(domain.SemanticEntityProduct),
		string(domain.SemanticTierValidatedClaim),
		string(domain.SemanticStatusActive),
		0.95,
		0,
		0,
		semanticRelationshipGroupKey(subjectEntityID, "uses", "", "Postgres", domain.PolarityPlus),
		int64(1),
		sql.NullTime{},
		sql.NullTime{},
		now,
		sql.NullTime{},
		now,
		now,
	)

	mock.ExpectQuery(`(?s)INSERT INTO semantic_relationship_records.*RETURNING`).
		WithArgs(
			teamID,
			ownerProfileID,
			subjectEntityID,
			"uses",
			string(domain.PolarityPlus),
			"",
			"Postgres",
			string(domain.SemanticEntityProduct),
			string(domain.SemanticTierValidatedClaim),
			string(domain.SemanticStatusActive),
			0.95,
			semanticRelationshipGroupKey(subjectEntityID, "uses", "", "Postgres", domain.PolarityPlus),
			"pending",
		).
		WillReturnRows(relationshipRows).
		RowsWillBeClosed()
	mock.ExpectExec(`INSERT INTO semantic_relationship_events`).
		WithArgs(
			teamID,
			relationshipID,
			"created",
			string(domain.SemanticTierValidatedClaim),
			string(domain.SemanticStatusActive),
			ownerProfileID,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	relationship, err := upsertSemanticRelationship(context.Background(), db, input, item, subjectEntityID, "")

	require.NoError(t, err)
	require.Equal(t, relationshipID, relationship.RelationshipID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpsertSemanticSearchDocumentClosesReturningRowsBeforeJobInsert(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	teamID := uuid.NewString()
	ownerProfileID := uuid.NewString()
	sourceID := uuid.NewString()
	searchDocumentID := uuid.NewString()

	documentRows := sqlmock.NewRows([]string{
		"search_document_id",
		"document_version",
		"search_state",
	}).AddRow(searchDocumentID, int64(1), "pending")

	mock.ExpectQuery(`(?s)INSERT INTO semantic_search_documents.*RETURNING`).
		WithArgs(
			teamID,
			ownerProfileID,
			"evidence",
			sourceID,
			"Dense-Mem uses Postgres.",
			int64(1),
		).
		WillReturnRows(documentRows).
		RowsWillBeClosed()
	mock.ExpectExec(`INSERT INTO semantic_embedding_jobs`).
		WithArgs(
			teamID,
			searchDocumentID,
			"evidence",
			sourceID,
			int64(1),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = upsertSemanticSearchDocument(context.Background(), db, semanticSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerProfileID,
		SourceType:     "evidence",
		SourceID:       sourceID,
		DocumentText:   " Dense-Mem uses Postgres. ",
	})

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpsertSemanticSearchDocumentSkipsJobWhenDocumentIsCurrent(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	teamID := uuid.NewString()
	ownerProfileID := uuid.NewString()
	sourceID := uuid.NewString()
	searchDocumentID := uuid.NewString()

	documentRows := sqlmock.NewRows([]string{
		"search_document_id",
		"document_version",
		"search_state",
	}).AddRow(searchDocumentID, int64(1), "current")

	mock.ExpectQuery(`(?s)INSERT INTO semantic_search_documents.*RETURNING`).
		WithArgs(
			teamID,
			ownerProfileID,
			"evidence",
			sourceID,
			"Dense-Mem uses Postgres.",
			int64(1),
		).
		WillReturnRows(documentRows).
		RowsWillBeClosed()

	err = upsertSemanticSearchDocument(context.Background(), db, semanticSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerProfileID,
		SourceType:     "evidence",
		SourceID:       sourceID,
		DocumentText:   " Dense-Mem uses Postgres. ",
	})

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimSemanticEmbeddingJobReclaimsExpiredLease(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewSemanticRepository(db, nil)
	teamID := uuid.NewString()
	jobID := uuid.NewString()
	searchDocumentID := uuid.NewString()
	sourceID := uuid.NewString()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH ready_team AS .*status = 'processing' AND lease_until <= now\(\).*FOR UPDATE SKIP LOCKED.*UPDATE semantic_embedding_jobs.*JOIN semantic_search_documents`).
		WithArgs(1).
		WillReturnRows(sqlmock.NewRows([]string{
			"team_id",
			"job_id",
			"search_document_id",
			"source_type",
			"source_id",
			"document_version",
			"attempts",
			"document_text",
		}).AddRow(teamID, jobID, searchDocumentID, "evidence", sourceID, int64(3), 2, "Recovered document")).
		RowsWillBeClosed()
	mock.ExpectCommit()

	jobs, err := repo.ClaimSemanticEmbeddingJobs(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	job := jobs[0]
	require.Equal(t, jobID, job.JobID)
	require.Equal(t, 2, job.Attempts)
	require.Equal(t, "Recovered document", job.DocumentText)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCompleteSemanticEmbeddingJobIgnoresStaleAttempt(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewSemanticRepository(db, nil)
	job := SemanticEmbeddingJob{
		TeamID:           uuid.NewString(),
		JobID:            uuid.NewString(),
		SearchDocumentID: uuid.NewString(),
		SourceType:       "evidence",
		SourceID:         uuid.NewString(),
		DocumentVersion:  1,
		Attempts:         1,
	}

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE semantic_embedding_jobs.*status = 'processing'.*attempts = \$[0-9]+`).
		WithArgs(job.TeamID, job.JobID, job.Attempts).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	err = repo.CompleteSemanticEmbeddingJob(context.Background(), job, []float32{0.1, 0.2}, "embedding-model")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFailSemanticEmbeddingJobIgnoresStaleAttempt(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewSemanticRepository(db, nil)
	job := SemanticEmbeddingJob{
		TeamID:           uuid.NewString(),
		JobID:            uuid.NewString(),
		SearchDocumentID: uuid.NewString(),
		SourceType:       "evidence",
		SourceID:         uuid.NewString(),
		DocumentVersion:  1,
		Attempts:         3,
	}

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE semantic_embedding_jobs.*status = 'processing'.*attempts = \$[0-9]+`).
		WithArgs("failed", "provider interrupted", job.TeamID, job.JobID, job.Attempts).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	err = repo.FailSemanticEmbeddingJob(context.Background(), job, errors.New("provider interrupted"))
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHydrateRecallEvidenceLoadsBoundedDiscoveryRelationships(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewSemanticRepository(db, nil)
	now := time.Date(2026, 7, 13, 15, 0, 0, 0, time.UTC)
	teamID := uuid.NewString()
	ownerProfileID := uuid.NewString()
	subjectEntityID := uuid.NewString()
	evidenceID := uuid.NewString()
	relationshipID := uuid.NewString()
	scope := domain.SemanticRecallSearchScope{
		TeamID: teamID,
		Features: domain.SemanticRecallQueryFeatures{
			Query:        "postgres memory",
			ContentQuery: "postgres memory",
			RelaxedQuery: "postgres OR memory",
		},
		ValidAt:                  now,
		KnownAt:                  now,
		KnownRelationshipIDs:     []string{},
		RelationshipsPerEvidence: 2,
	}

	evidenceRows := sqlmock.NewRows([]string{
		"team_id", "fragment_id", "owner_profile_id", "content", "source", "source_doc_id",
		"source_group", "source_type", "authority", "labels", "metadata", "content_hash",
		"idempotency_key", "embedding_model", "embedding_contract_id", "created_at",
	}).AddRow(
		teamID, evidenceID, ownerProfileID, "Dense-Mem uses Postgres.", "test", "doc-1",
		"wiki", string(domain.SourceTypeDocument), string(domain.AuthorityPrimary),
		pq.StringArray{}, "{}", "hash", "idem", "", "", now,
	)
	relationshipColumns := []string{
		"fragment_id",
		"team_id",
		"relationship_id",
		"owner_profile_id",
		"owner_profile_name",
		"subject_entity_id",
		"subject_entity_name",
		"subject_entity_kind",
		"predicate",
		"polarity",
		"object_entity_id",
		"object_entity_name",
		"object_entity_kind",
		"object_value",
		"object_kind",
		"tier",
		"status",
		"confidence",
		"support_count",
		"source_group_count",
		"semantic_group_key",
		"primary_source_group",
		"version",
		"valid_from",
		"valid_to",
		"recorded_at",
		"recorded_to",
		"created_at",
		"updated_at",
		"evidence_index",
		"quote",
		"support_created_at",
	}
	relationshipRows := sqlmock.NewRows(relationshipColumns).
		AddRow(
			evidenceID,
			teamID, relationshipID, ownerProfileID, "Mark", subjectEntityID,
			"Dense-Mem", string(domain.SemanticEntityProject), "uses", string(domain.PolarityPlus),
			"", "", "", "Postgres", string(domain.SemanticEntityProduct),
			string(domain.SemanticTierFact), string(domain.SemanticStatusActive), 0.95,
			3, 2, "group-1", "wiki", 4,
			sql.NullTime{}, sql.NullTime{}, now, sql.NullTime{}, now, now,
			0, "Dense-Mem uses Postgres.", now,
		)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM semantic_evidence_fragments e.*e\.fragment_id = ANY`).
		WithArgs(now, now, teamID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(evidenceRows).
		RowsWillBeClosed()
	mock.ExpectQuery(`(?s)FROM semantic_relationship_supports support.*ranked.per_evidence_rank <= p.per_evidence`).
		WithArgs("postgres OR memory", now, now, 2, teamID, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), teamID).
		WillReturnRows(relationshipRows).
		RowsWillBeClosed()
	mock.ExpectCommit()

	results, err := repo.HydrateRecallEvidence(context.Background(), scope, []string{evidenceID}, []string{relationshipID})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, evidenceID, results[0].Evidence.FragmentID)
	require.Len(t, results[0].Relationships, 1)
	require.Equal(t, relationshipID, results[0].Relationships[0].RelationshipID)
	require.Equal(t, "Dense-Mem", results[0].Relationships[0].SubjectEntityName)
	require.Equal(t, "Mark", results[0].Relationships[0].OwnerProfileName)
	require.Len(t, results[0].Supports, 1)
	require.Equal(t, evidenceID, results[0].Supports[0].FragmentID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRecallHypothesesReadsPostgresRows(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := NewSemanticRepository(db, nil)
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	hypothesisID := uuid.NewString()
	now := time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM semantic_hypotheses.*status IN \('proposed', 'reinforced'\)`).
		WithArgs(teamID, "%postgres%", "%postgres%", "%postgres%", "%postgres%", "%postgres%", 5).
		WillReturnRows(sqlmock.NewRows([]string{
			"hypothesis_id", "owner_profile_id", "text", "status", "source_refs", "metadata", "created_at", "updated_at",
		}).AddRow(
			hypothesisID,
			ownerID,
			"Postgres recall may improve relationship grounding.",
			string(domain.SemanticHypothesisProposed),
			[]byte(`[{"type":"relationship","id":"rel-1"}]`),
			[]byte(`{"what_if":"What if related edges are traversed?","confidence":0.7}`),
			now,
			now,
		)).
		RowsWillBeClosed()
	mock.ExpectCommit()

	got, err := repo.RecallHypotheses(context.Background(), teamID, "postgres", 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, hypothesisID, got[0].DreamID)
	require.Equal(t, ownerID, got[0].ProfileID)
	require.Equal(t, domain.DreamStatusProposed, got[0].Status)
	require.Equal(t, "What if related edges are traversed?", got[0].WhatIf)
	require.Equal(t, 0.7, got[0].Confidence)
	require.Equal(t, []domain.DreamSourceRef{{Type: "relationship", ID: "rel-1"}}, got[0].SourceRefs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNormalizeSemanticLimit(t *testing.T) {
	cases := map[int]int{
		0:   10,
		-1:  10,
		1:   1,
		100: 50,
	}
	for input, want := range cases {
		if got := normalizeSemanticLimit(input); got != want {
			t.Fatalf("normalizeSemanticLimit(%d) = %d, want %d", input, got, want)
		}
	}
}
