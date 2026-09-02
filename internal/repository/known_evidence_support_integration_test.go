package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestKnownEvidenceSupportPreservesABCOwnershipAndIsolation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "known-support-isolation-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "known-support-isolation-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "known-support-isolation-owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "known-support-isolation-team-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "known-support-isolation-owner-c")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamA, ownerB, "project", "Known Support Subject")
	object := createSemanticEntity(t, ctx, semantic, teamA, ownerB, "product", "Known Support Object")
	known := createSemanticIngest(t, ctx, ledger, teamA, ownerA,
		"owner-a-known-support", "Owner A's shared evidence supports Owner B's relationship.")
	submitted := createSemanticIngest(t, ctx, ledger, teamA, ownerB,
		"owner-b-submitted-support", "Owner B submits the relationship and retains direct support.")

	decision, err := semantic.ApplyRelationshipDecision(ctx, ApplyRelationshipDecisionInput{
		TeamID:          teamA,
		OwnerProfileID:  ownerB,
		IngestID:        submitted.IngestID,
		SubjectEntityID: subject.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  object.EntityID,
		EvidenceVerdict: string(domain.VerificationEntailed),
		Support: &EvidenceSupportInput{
			FragmentID:     submitted.Evidence[0].FragmentID,
			SourceGroupKey: "known-support-submitted",
			SpanStart:      0,
			SpanEnd:        len([]rune(submitted.Evidence[0].Content)),
			Authority:      "primary",
		},
		Supports: []EvidenceSupportInput{{
			FragmentID:             known.Evidence[0].FragmentID,
			EvidenceOwnerProfileID: ownerA,
			SourceGroupKey:         "known-support-owner-a",
			SpanStart:              0,
			SpanEnd:                len([]rune(known.Evidence[0].Content)),
			Authority:              "primary",
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	require.NotNil(t, decision.Relationship)
	require.Equal(t, ownerB, decision.Relationship.OwnerProfileID)

	type supportOwnerRow struct {
		owner         string
		evidenceOwner string
		fragment      string
	}
	var supportRows []supportOwnerRow
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT owner_profile_id::text, evidence_owner_profile_id::text, fragment_id::text
			FROM relationship_evidence_supports
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
			ORDER BY created_at, support_id
		`, teamA, decision.Relationship.RelationshipID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row supportOwnerRow
			if err := rows.Scan(&row.owner, &row.evidenceOwner, &row.fragment); err != nil {
				return err
			}
			supportRows = append(supportRows, row)
		}
		return rows.Err()
	}))
	require.Len(t, supportRows, 2)
	for _, row := range supportRows {
		require.Equal(t, ownerB, row.owner, "relationship and support-decision ownership must remain with B")
		if row.fragment == known.Evidence[0].FragmentID {
			require.Equal(t, ownerA, row.evidenceOwner, "known evidence provenance must remain with A")
		} else {
			require.Equal(t, ownerB, row.evidenceOwner, "submitted evidence provenance must remain with B")
		}
	}

	trace, err := semantic.TraceRelationship(ctx, TraceRelationshipInput{TeamID: teamA, RelationshipID: decision.Relationship.RelationshipID})
	require.NoError(t, err)
	require.Len(t, trace.EvidenceSupports, 2)
	for _, support := range trace.EvidenceSupports {
		if support.FragmentID == known.Evidence[0].FragmentID {
			require.Equal(t, ownerA, support.EvidenceOwnerProfileID)
		}
	}

	_, err = semantic.RetractRelationship(ctx, RetractRelationshipInput{
		TeamID: teamA, OwnerProfileID: ownerA, RelationshipID: decision.Relationship.RelationshipID,
		Reason: "A must not mutate B's relationship", IdempotencyKey: "known-support-retract-a",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSemanticOwnerMismatch), err)

	knownSupportID := ""
	for _, row := range supportRows {
		if row.fragment == known.Evidence[0].FragmentID {
			require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
				return tx.Raw(`
					SELECT support_id::text
					FROM relationship_evidence_supports
					WHERE team_id = ?::uuid AND relationship_id = ?::uuid AND fragment_id = ?::uuid
				`, teamA, decision.Relationship.RelationshipID, row.fragment).Row().Scan(&knownSupportID)
			}))
			break
		}
	}
	require.NotEmpty(t, knownSupportID)
	for _, actor := range []struct {
		teamID    string
		profileID string
		label     string
	}{
		{teamA, ownerA, "A"},
		{teamC, ownerC, "C"},
	} {
		_, err = semantic.ApplyRelationshipSupportDecision(ctx, ApplyRelationshipSupportDecisionInput{
			TeamID: actor.teamID, OwnerProfileID: actor.profileID,
			RelationshipID: decision.Relationship.RelationshipID, SupportID: knownSupportID,
			Decision: "revoke", Reason: actor.label + " must not mutate B's relationship",
			IdempotencyKey: "known-support-revoke-" + actor.label,
		})
		require.Error(t, err, actor.label)
	}

	privateSpace, err := NewMemorySpaceRepository(appDB, rls).EnsureProfilePrivate(ctx, uuid.MustParse(teamA), uuid.MustParse(ownerA))
	require.NoError(t, err)
	privateCtx := requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{{ID: privateSpace.ID, Kind: domain.MemorySpaceProfilePrivate}})
	private, err := createTestIngest(privateCtx, ledger, CreateIngestInput{
		TeamID: teamA, OwnerProfileID: ownerA, SpaceID: privateSpace.ID.String(), SpaceGeneration: privateSpaceGeneration(t, ctx, adminDB, rls, privateSpace.ID),
		IdempotencyKey: "known-support-private", RequestHash: "known-support-private-hash",
		Evidence: []EvidenceInput{{Content: "Private evidence must not support B's relationship."}},
	})
	require.NoError(t, err)
	_, err = semantic.ApplyRelationshipDecision(ctx, ApplyRelationshipDecisionInput{
		TeamID: teamA, OwnerProfileID: ownerB, IngestID: submitted.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		EvidenceVerdict: string(domain.VerificationEntailed),
		Support:         &EvidenceSupportInput{FragmentID: submitted.Evidence[0].FragmentID, SourceGroupKey: "known-support-private-submitted", SpanStart: 0, SpanEnd: len([]rune(submitted.Evidence[0].Content)), Authority: "primary"},
		Supports:        []EvidenceSupportInput{{FragmentID: private.Evidence[0].FragmentID, EvidenceOwnerProfileID: ownerA, SourceGroupKey: "known-support-private", SpanStart: 0, SpanEnd: len([]rune(private.Evidence[0].Content)), Authority: "primary"}},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSemanticOwnerMismatch), err)

	otherTeam := createSemanticIngest(t, ctx, ledger, teamC, ownerC, "known-support-cross-team", "Cross-team evidence must not support B's relationship.")
	_, err = semantic.ApplyRelationshipDecision(ctx, ApplyRelationshipDecisionInput{
		TeamID: teamA, OwnerProfileID: ownerB, IngestID: submitted.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		EvidenceVerdict: string(domain.VerificationEntailed),
		Support:         &EvidenceSupportInput{FragmentID: submitted.Evidence[0].FragmentID, SourceGroupKey: "known-support-cross-team-submitted", SpanStart: 0, SpanEnd: len([]rune(submitted.Evidence[0].Content)), Authority: "primary"},
		Supports:        []EvidenceSupportInput{{FragmentID: otherTeam.Evidence[0].FragmentID, EvidenceOwnerProfileID: ownerC, SourceGroupKey: "known-support-cross-team", SpanStart: 0, SpanEnd: len([]rune(otherTeam.Evidence[0].Content)), Authority: "primary"}},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSemanticOwnerMismatch), err)
}

func TestKnownEvidenceSupportRejectsCrossSpacePrivateRelationship(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "known-support-cross-space")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "known-support-cross-space-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "known-support-cross-space-owner-b")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	sharedSpace, err := NewMemorySpaceRepository(appDB, rls).GetTeamShared(ctx, uuid.MustParse(teamID))
	require.NoError(t, err)
	require.NotNil(t, sharedSpace)
	privateSpace, err := NewMemorySpaceRepository(appDB, rls).EnsureProfilePrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerB))
	require.NoError(t, err)
	privateCtx := requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{
		{ID: sharedSpace.ID, Kind: domain.MemorySpaceTeamShared},
		{ID: privateSpace.ID, Kind: domain.MemorySpaceProfilePrivate},
	})
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerB, "project", "Cross-space Subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerB, "product", "Cross-space Object")
	known := createSemanticIngest(t, ctx, ledger, teamID, ownerA,
		"known-support-cross-space-known", "Shared known evidence must not support a private relationship.")
	submitted, err := createTestIngest(privateCtx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerB, SpaceID: privateSpace.ID.String(),
		SpaceGeneration: privateSpaceGeneration(t, ctx, adminDB, rls, privateSpace.ID),
		IdempotencyKey:  "known-support-cross-space-submitted", RequestHash: "known-support-cross-space-submitted-hash",
		Evidence: []EvidenceInput{{Content: "Private submitted evidence for the relationship."}},
	})
	require.NoError(t, err)
	require.Len(t, submitted.Evidence, 1)

	_, err = semantic.ApplyRelationshipDecision(privateCtx, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerB, IngestID: submitted.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		EvidenceVerdict: string(domain.VerificationEntailed),
		Support: &EvidenceSupportInput{
			FragmentID: submitted.Evidence[0].FragmentID, SourceGroupKey: "known-support-cross-space-submitted",
			SpanStart: 0, SpanEnd: len([]rune(submitted.Evidence[0].Content)), Authority: "primary",
		},
		Supports: []EvidenceSupportInput{{
			FragmentID: known.Evidence[0].FragmentID, EvidenceOwnerProfileID: ownerA,
			SourceGroupKey: "known-support-cross-space-known", SpanStart: 0,
			SpanEnd: len([]rune(known.Evidence[0].Content)), Authority: "primary",
		}},
	})
	require.ErrorIs(t, err, ErrSemanticOwnerMismatch)

	var supportCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM relationship_evidence_supports
			WHERE team_id = ?::uuid AND fragment_id = ?::uuid
		`, teamID, known.Evidence[0].FragmentID).Scan(&supportCount).Error
	}))
	require.Zero(t, supportCount, "cross-space known evidence must not be attached")
}

func TestKnownEvidenceSupportAllowsOwnerCorrection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "known-support-correction", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "known-support-correction")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "known-support-correction-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "known-support-correction-owner-b")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerB, "person", "Correction Subject")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerB, "project", "Correction Wrong")
	correctObject := createSemanticEntity(t, ctx, semantic, teamID, ownerB, "project", "Correction Correct")
	known := createSemanticIngest(t, ctx, ledger, teamID, ownerA,
		"known-support-correction-known", "Correction Subject works on Correction Correct.")
	submitted := createSemanticIngest(t, ctx, ledger, teamID, ownerB,
		"known-support-correction-submitted", "Correction Subject works on Correction Wrong.")
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerB, IngestID: submitted.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		EvidenceVerdict: string(domain.VerificationEntailed),
		Support: &EvidenceSupportInput{
			FragmentID: submitted.Evidence[0].FragmentID, SourceGroupKey: "known-support-correction-submitted",
			SpanStart: 0, SpanEnd: len([]rune(submitted.Evidence[0].Content)), Authority: "primary",
		},
		Supports: []EvidenceSupportInput{{
			FragmentID: known.Evidence[0].FragmentID, EvidenceOwnerProfileID: ownerA,
			SourceGroupKey: "known-support-correction-known", SpanStart: 0,
			SpanEnd: len([]rune(known.Evidence[0].Content)), Authority: "primary",
		}},
	}).Relationship

	corrected, err := correctRelationshipWithTestEmbeddings(ctx, semantic, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerB, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch: RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{
			{EvidenceID: known.Evidence[0].FragmentID, Start: 0, End: len([]rune(known.Evidence[0].Content))},
			{EvidenceID: submitted.Evidence[0].FragmentID, Start: 0, End: len([]rune(submitted.Evidence[0].Content))},
		},
		Reason: "correct the object using shared known evidence", IdempotencyKey: "known-support-correction-submit",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", corrected.ProcessingState)

	type observationOwnership struct {
		ingestOwner   string
		evidenceOwner string
	}
	var observations []observationOwnership
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT ingest.owner_profile_id::text, support.evidence_owner_profile_id::text
			FROM relationship_observations AS observation
			JOIN knowledge_ingests AS ingest
			  ON ingest.team_id = observation.team_id AND ingest.ingest_id = observation.ingest_id
			JOIN relationship_evidence_supports AS support
			  ON support.team_id = observation.team_id AND support.observation_id = observation.observation_id
			WHERE observation.team_id = ?::uuid AND observation.relationship_id = ?::uuid
			ORDER BY support.created_at, support.support_id
		`, teamID, corrected.Correction.SuccessorRelationshipID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ownership observationOwnership
			if err := rows.Scan(&ownership.ingestOwner, &ownership.evidenceOwner); err != nil {
				return err
			}
			observations = append(observations, ownership)
		}
		return rows.Err()
	}))
	require.Len(t, observations, 2)
	for _, ownership := range observations {
		require.Equal(t, ownerB, ownership.ingestOwner, "successor observations must use an owner-B ingest")
	}
	owners := map[string]bool{}
	for _, ownership := range observations {
		owners[ownership.evidenceOwner] = true
	}
	require.True(t, owners[ownerA], "known evidence provenance must remain with owner A")
	require.True(t, owners[ownerB], "submitted evidence provenance must remain with owner B")
}

func TestKnownEvidenceFenceSerializesConcurrentQuarantine(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "known-support-quarantine-race")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "known-support-quarantine-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	known := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "known-support-quarantine-source", "Evidence held by the known-evidence fence.")
	loaded, err := semantic.ListSubmissionAssessmentKnownEvidence(ctx, SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, EvidenceIDs: []string{known.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, loaded.Evidence, 1)
	commitInput := CommitSubmissionAssessmentInput{
		RememberCommitScope:   RememberCommitScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString()},
		KnownEvidenceSnapshot: loaded.Evidence,
	}

	commitReady := make(chan struct{})
	releaseCommit := make(chan struct{})
	commitErr := make(chan error, 1)
	go func() {
		commitErr <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			if err := reauthorizeSubmissionKnownEvidence(ctx, tx, commitInput); err != nil {
				return err
			}
			close(commitReady)
			<-releaseCommit
			return nil
		})
	}()
	<-commitReady

	quarantineStarted := make(chan struct{})
	quarantineErr := make(chan error, 1)
	go func() {
		quarantineErr <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			close(quarantineStarted)
			return insertEvidenceQuarantine(ctx, tx, CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerID}, known.IngestID, known.Evidence[0].FragmentID, "concurrent quarantine")
		})
	}()
	<-quarantineStarted
	select {
	case err := <-quarantineErr:
		t.Fatalf("quarantine completed before the known-evidence fence released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseCommit)
	require.NoError(t, <-commitErr)
	require.NoError(t, <-quarantineErr)
	var quarantineCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM evidence_quarantines
			WHERE team_id = ?::uuid AND fragment_id = ?::uuid AND status = 'active'
		`, teamID, known.Evidence[0].FragmentID).Scan(&quarantineCount).Error
	}))
	require.EqualValues(t, 1, quarantineCount)
	commitFenceErr := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return reauthorizeSubmissionKnownEvidence(ctx, tx, commitInput)
	})
	require.ErrorIs(t, commitFenceErr, ErrSubmissionAssessmentKnownEvidenceStale, "anchor-only known evidence must fail reauthorization after quarantine")
}

func TestKnownEvidenceFenceAllowsVisibleCrossOwnerEvidenceUnderRLS(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "known-support-cross-owner-fence")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "known-support-cross-owner-fence-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "known-support-cross-owner-fence-owner-b")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	known := createSemanticIngest(t, ctx, ledger, teamID, ownerA, "known-support-cross-owner-fence-known", "Visible shared evidence belongs to owner A.")

	loaded, err := semantic.ListSubmissionAssessmentKnownEvidence(ctx, SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerB, EvidenceIDs: []string{known.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, loaded.Evidence, 1)
	require.Equal(t, ownerA, loaded.Evidence[0].OwnerProfileID)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerB, func(tx *gorm.DB) error {
		return reauthorizeSubmissionKnownEvidence(ctx, tx, CommitSubmissionAssessmentInput{
			RememberCommitScope:   RememberCommitScope{TeamID: teamID, OwnerProfileID: ownerB, IngestID: uuid.NewString()},
			KnownEvidenceSnapshot: loaded.Evidence,
		})
	})
	require.NoError(t, err)
}

func TestKnownEvidenceFenceFailsClosedWhenSourceRevisionIsLocked(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "known-support-source-lock")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "known-support-source-lock-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	known, err := createTestIngest(ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "known-source-lock", RequestHash: "known-source-lock-hash",
		Evidence: []EvidenceInput{{
			Content: "Source-backed known evidence.", SourceKey: "document://known-source-lock", SourceRevisionToken: "rev-1",
		}},
	})
	require.NoError(t, err)
	require.Len(t, known.Evidence, 1)
	loaded, err := semantic.ListSubmissionAssessmentKnownEvidence(ctx, SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, EvidenceIDs: []string{known.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, loaded.Evidence, 1)

	lockHeld := make(chan struct{})
	releaseLock := make(chan struct{})
	lockErr := make(chan error, 1)
	go func() {
		lockErr <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			var sourceID string
			if err := tx.Raw(`
				SELECT source_id::text
				FROM evidence_sources
				WHERE team_id = ?::uuid AND source_id = ?::uuid AND owner_profile_id = ?::uuid
				FOR UPDATE
			`, teamID, known.Evidence[0].SourceID, ownerID).Row().Scan(&sourceID); err != nil {
				return err
			}
			close(lockHeld)
			<-releaseLock
			return nil
		})
	}()
	<-lockHeld
	commitErr := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return reauthorizeSubmissionKnownEvidence(ctx, tx, CommitSubmissionAssessmentInput{
			RememberCommitScope:   RememberCommitScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString()},
			KnownEvidenceSnapshot: loaded.Evidence,
		})
	})
	require.ErrorIs(t, commitErr, ErrSubmissionAssessmentKnownEvidenceStale)
	close(releaseLock)
	require.NoError(t, <-lockErr)
}

func TestKnownEvidenceFenceFailsClosedWhenPrivateSpaceSeals(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "known-support-space-lock")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "known-support-space-lock-owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	privateSpace, err := NewMemorySpaceRepository(appDB, rls).EnsureProfilePrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
	require.NoError(t, err)
	generation := privateSpaceGeneration(t, ctx, adminDB, rls, privateSpace.ID)
	privateCtx := requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{{ID: privateSpace.ID, Kind: domain.MemorySpaceProfilePrivate}})
	known, err := createTestIngest(privateCtx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: privateSpace.ID.String(), SpaceGeneration: generation,
		IdempotencyKey: "known-space-lock", RequestHash: "known-space-lock-hash",
		Evidence: []EvidenceInput{{Content: "Private known evidence becomes stale when its space seals."}},
	})
	require.NoError(t, err)
	require.Len(t, known.Evidence, 1)
	loaded, err := semantic.ListSubmissionAssessmentKnownEvidence(privateCtx, SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, EvidenceIDs: []string{known.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, loaded.Evidence, 1)
	commitInput := CommitSubmissionAssessmentInput{
		RememberCommitScope:   RememberCommitScope{TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString()},
		KnownEvidenceSnapshot: loaded.Evidence,
	}
	require.NoError(t, rls.WithTeamProfileTx(privateCtx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return reauthorizeSubmissionKnownEvidence(ctx, tx, commitInput)
	}), "visible private known evidence must reauthorize before the space race")

	sealReady := make(chan struct{})
	releaseSeal := make(chan struct{})
	sealErr := make(chan error, 1)
	released := false
	defer func() {
		if !released {
			close(releaseSeal)
		}
	}()
	go func() {
		sealErr <- rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			result := tx.Exec(`
				UPDATE memory_spaces
				SET lifecycle_state = 'sealed', generation = generation + 1,
				    sealed_at = now(), updated_at = now()
				WHERE team_id = ?::uuid AND id = ?::uuid AND lifecycle_state = 'active'
			`, teamID, privateSpace.ID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("private known-evidence space was not sealed")
			}
			close(sealReady)
			<-releaseSeal
			return nil
		})
	}()
	select {
	case <-sealReady:
	case err := <-sealErr:
		require.NoError(t, err)
		return
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for private-space seal")
	}

	commitErr := rls.WithTeamProfileTx(privateCtx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return reauthorizeSubmissionKnownEvidence(ctx, tx, commitInput)
	})
	close(releaseSeal)
	released = true
	require.ErrorIs(t, commitErr, ErrSubmissionAssessmentKnownEvidenceStale)
	require.NoError(t, <-sealErr)
}

func privateSpaceGeneration(t *testing.T, ctx context.Context, db *gorm.DB, rls *storagepostgres.RLS, spaceID uuid.UUID) int64 {
	t.Helper()
	var generation int64
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, spaceID).Row().Scan(&generation)
	}))
	return generation
}
