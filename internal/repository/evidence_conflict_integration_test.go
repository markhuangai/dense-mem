package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func citedEvidenceRememberInput(teamID, ownerID, label, firstContent, secondContent, spaceID string, generation int64) SynchronousRememberCommitInput {
	firstID, secondID := uuid.NewString(), uuid.NewString()
	assessmentID := uuid.NewString()
	input := SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: uuid.NewString(),
		SpaceID: spaceID, SpaceGeneration: generation, IdempotencyKey: label, RequestHash: sha256Hex(label), SourceSummary: label,
		Evidence: []EvidenceInput{
			{FragmentID: firstID, Content: firstContent, ContentHash: sha256Hex(firstContent), SourceType: "manual", Authority: "primary"},
			{FragmentID: secondID, Content: secondContent, ContentHash: sha256Hex(secondContent), SourceType: "manual", Authority: "secondary"},
		},
		AssessmentID: assessmentID, AssessmentJSON: json.RawMessage(`{"request_id":"` + label + `"}`), ProviderTurns: 1,
		EvidenceSecurityResults: []EvidenceSecurityResult{
			{FragmentID: firstID, EvidenceID: "evidence:0", EvidenceIndex: 0, Decision: "pass", Safe: true},
			{FragmentID: secondID, EvidenceID: "evidence:1", EvidenceIndex: 1, Decision: "pass", Safe: true},
		},
	}
	input.Commit = CommitSubmissionAssessmentInput{
		AssessmentID: assessmentID,
		Items:        []SubmissionAssessmentItemInput{{FragmentID: firstID, EvidenceID: "evidence:0"}, {FragmentID: secondID, EvidenceID: "evidence:1"}},
		EvidenceConflictResults: []EvidenceConflictResultInput{{Positions: []EvidenceConflictPositionInput{
			{EvidenceID: "evidence:0", Start: 0, End: 5},
			{EvidenceID: "evidence:1", Start: 0, End: 5},
		}}},
		Payload: map[string]any{"response_hash": sha256Hex(label), "model": "test-model", "tokenizer": "o200k_base", "candidate_context_tokens": 0, "candidate_context_truncated": false},
	}
	return input
}

func commitCitedEvidenceFixture(t *testing.T, ctx context.Context, repo *LedgerRepositoryImpl, input SynchronousRememberCommitInput) *SynchronousRememberCommitResult {
	t.Helper()
	plan, err := repo.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	result, err := repo.CommitRememberWithEmbeddings(ctx, input, rememberTestEmbeddings(plan, false))
	require.NoError(t, err)
	return result
}

func TestEvidenceConflictRememberCreationAndRecurrence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-conflict-recurrence", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-conflict-recurrence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-conflict-recurrence-owner")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)

	first := citedEvidenceRememberInput(teamID, ownerID, "evidence-conflict-first", "Alice approved the change.", "Alice rejected the change.", spaceID, generation)
	commitCitedEvidenceFixture(t, ctx, repo, first)

	detail, err := repo.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: teamID, ConflictID: conflictIDForTest(t, adminDB, rls, teamID), EventLimit: 10})
	require.NoError(t, err)
	require.NotNil(t, detail.Conflict)
	require.Equal(t, "open", detail.Conflict.Status)
	require.Equal(t, 1, detail.Conflict.Version)
	require.Len(t, detail.Conflict.Positions, 2)
	require.Len(t, detail.Conflict.Events, 1)
	require.Equal(t, "opened", detail.Conflict.Events[0].Action)
	require.Equal(t, "Alice", detail.Conflict.Positions[0].Quote)
	listed, err := repo.ListEvidenceConflicts(ctx, EvidenceConflictListInput{TeamID: teamID, Status: "open", Limit: 25})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	require.Len(t, listed.Items[0].Positions, 2)

	recalled, err := NewSearchRepository(appDB, rls).RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID, Query: "approved", Limit: 10,
	})
	require.NoError(t, err)
	require.NotEmpty(t, recalled.Results)
	require.Len(t, recalled.EvidenceConflicts, 1, "team-shared recall without an explicit space must project cited conflicts")
	require.Equal(t, detail.Conflict.ConflictID, recalled.EvidenceConflicts[0].ConflictID)

	second := citedEvidenceRememberInput(teamID, ownerID, "evidence-conflict-second", "Alice approved the change.", "Alice rejected the change.", spaceID, generation)
	duplicateInput := duplicateCandidateInput(second)
	duplicatePlan, err := repo.PlanRememberDuplicateEmbeddings(ctx, duplicateInput)
	require.NoError(t, err)
	duplicateResolution, err := repo.ResolveRememberDuplicateCandidates(ctx, duplicateInput, duplicatePlanEmbeddings(duplicatePlan))
	require.NoError(t, err)
	second.DuplicateResolutions = duplicateResolution.Exact
	commitCitedEvidenceFixture(t, ctx, repo, second)
	detail, err = repo.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: teamID, ConflictID: detail.Conflict.ConflictID, EventLimit: 10})
	require.NoError(t, err)
	require.Len(t, detail.Conflict.Events, 2)
	_, err = repo.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: uuid.NewString(), ConflictID: detail.Conflict.ConflictID, EventLimit: 10})
	require.ErrorIs(t, err, ErrEvidenceConflictNotFound)
	_, err = repo.ResolveEvidenceConflict(ctx, EvidenceConflictResolutionInput{
		TeamID: uuid.NewString(), ConflictID: detail.Conflict.ConflictID, ExpectedVersion: 2,
		Decision: "resolve", Reason: "wrong-team attempt", ActorKind: "control",
	})
	require.ErrorIs(t, err, ErrEvidenceConflictNotFound)
	_, err = repo.ResolveEvidenceConflict(ctx, EvidenceConflictResolutionInput{
		TeamID: teamID, ConflictID: detail.Conflict.ConflictID, ExpectedVersion: 1,
		Decision: "resolve", Reason: "stale attempt", ActorKind: "control",
	})
	require.ErrorIs(t, err, ErrEvidenceConflictVersionStale)

	var versions []int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`SELECT version FROM evidence_conflict_cases WHERE team_id = ?::uuid ORDER BY created_at`, teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var version int
			if err := rows.Scan(&version); err != nil {
				return err
			}
			versions = append(versions, version)
		}
		return rows.Err()
	}))
	require.Equal(t, []int{2}, versions)
	var eventCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM evidence_conflict_events WHERE team_id = ?::uuid`, teamID).Row().Scan(&eventCount)
	}))
	require.EqualValues(t, 2, eventCount)
	var eventTimes []time.Time
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`SELECT created_at FROM evidence_conflict_events WHERE team_id = ?::uuid ORDER BY ordinal`, teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var createdAt time.Time
			if err := rows.Scan(&createdAt); err != nil {
				return err
			}
			eventTimes = append(eventTimes, createdAt)
		}
		return rows.Err()
	}))
	require.Len(t, eventTimes, 2)
	for _, event := range detail.Conflict.Events {
		for _, position := range event.CitationSnapshot {
			require.NotEmpty(t, position.PositionID, "recurrence snapshots must reference immutable case positions")
		}
	}
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		historical, err := loadRecallEvidenceConflictCase(ctx, tx, teamID, detail.Conflict.ConflictID, &eventTimes[0])
		if err != nil {
			return err
		}
		require.Equal(t, "open", historical.Status)
		require.Equal(t, 1, historical.Version)
		return nil
	}))
	resolved, err := repo.ResolveEvidenceConflict(ctx, EvidenceConflictResolutionInput{
		TeamID: teamID, ConflictID: detail.Conflict.ConflictID, ExpectedVersion: 2,
		Decision: "resolve", Reason: "reviewed cited positions",
		PreferredPositionID: detail.Conflict.Positions[0].PositionID,
		ActorKind:           "control", ActorID: "integration-test",
	})
	require.NoError(t, err)
	require.Equal(t, "resolved", resolved.Status)
	require.Equal(t, 3, resolved.Version)

	third := citedEvidenceRememberInput(teamID, ownerID, "evidence-conflict-terminal-recurrence", "Alice approved the change.", "Alice rejected the change.", spaceID, generation)
	thirdDuplicateInput := duplicateCandidateInput(third)
	thirdDuplicatePlan, err := repo.PlanRememberDuplicateEmbeddings(ctx, thirdDuplicateInput)
	require.NoError(t, err)
	thirdDuplicateResolution, err := repo.ResolveRememberDuplicateCandidates(ctx, thirdDuplicateInput, duplicatePlanEmbeddings(thirdDuplicatePlan))
	require.NoError(t, err)
	third.DuplicateResolutions = thirdDuplicateResolution.Exact
	commitCitedEvidenceFixture(t, ctx, repo, third)
	terminal, err := repo.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: teamID, ConflictID: detail.Conflict.ConflictID, EventLimit: 10})
	require.NoError(t, err)
	require.Equal(t, "resolved", terminal.Conflict.Status)
	require.Equal(t, 3, terminal.Conflict.Version, "terminal recurrence must not reopen or increment the case")
	require.Len(t, terminal.Conflict.Events, 4)
	require.Equal(t, "recited", terminal.Conflict.Events[0].Action)
	require.Equal(t, 3, terminal.Conflict.Events[0].CaseVersion)
}

func TestEvidenceConflictDismissalRecordsTerminalEvent(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-conflict-dismiss", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-conflict-dismiss-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-conflict-dismiss-owner")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)

	input := citedEvidenceRememberInput(teamID, ownerID, "evidence-conflict-dismiss", "Dismissal evidence supports the first position.", "Dismissal evidence supports the opposing position.", spaceID, generation)
	commitCitedEvidenceFixture(t, ctx, repo, input)
	conflictID := conflictIDForTest(t, adminDB, rls, teamID)

	dismissed, err := repo.ResolveEvidenceConflict(ctx, EvidenceConflictResolutionInput{
		TeamID: teamID, ConflictID: conflictID, ExpectedVersion: 1,
		Decision: "dismiss", Reason: "reviewer determined the citations are not actionable",
		ActorKind: "control", ActorID: "integration-test",
	})
	require.NoError(t, err)
	require.Equal(t, "dismissed", dismissed.Status)
	require.Equal(t, 2, dismissed.Version)

	detail, err := repo.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: teamID, ConflictID: conflictID, EventLimit: 10})
	require.NoError(t, err)
	require.Len(t, detail.Conflict.Events, 2)
	require.Equal(t, "dismissed", detail.Conflict.Events[0].Action)
	require.Len(t, detail.Conflict.Events[0].CitationSnapshot, 2)
	require.NotEmpty(t, detail.Conflict.Events[0].CitationSnapshot[0].PositionID)

	recalled, err := NewSearchRepository(appDB, rls).RecallEvidence(ctx, RecallEvidenceInput{TeamID: teamID, Query: "Dismissal evidence", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, recalled.EvidenceConflicts)
}

func TestEvidenceConflictRecallBoundsCaseHydration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-conflict-recall-bound", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-conflict-recall-bound-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-conflict-recall-bound-owner")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)

	sharedContent := "The bounded recall citation is shared across cases."
	var sharedEvidenceID string
	for index := 0; index < EvidenceConflictMaxResults+1; index++ {
		input := citedEvidenceRememberInput(
			teamID,
			ownerID,
			fmt.Sprintf("evidence-conflict-recall-bound-%d", index),
			sharedContent,
			fmt.Sprintf("The opposing citation is case %d.", index),
			spaceID,
			generation,
		)
		commitCitedEvidenceFixture(t, ctx, repo, input)
		if index == 0 {
			sharedEvidenceID = input.Evidence[0].FragmentID
		}
	}

	var expectedIDs []string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT conflict_id::text
			FROM evidence_conflict_cases
			WHERE team_id = ?::uuid
			ORDER BY updated_at DESC, conflict_id DESC
			LIMIT ?
		`, teamID, EvidenceConflictMaxResults).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var conflictID string
			if err := rows.Scan(&conflictID); err != nil {
				return err
			}
			expectedIDs = append(expectedIDs, conflictID)
		}
		return rows.Err()
	}))

	var records []EvidenceConflictCaseRecord
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		loaded, err := loadRecallEvidenceConflictRecords(ctx, tx, RecallEvidenceInput{
			TeamID: teamID, SpaceID: spaceID, SpaceKind: "team_shared",
		}, []RecallEvidenceHit{{EvidenceID: sharedEvidenceID}})
		if err != nil {
			return err
		}
		records = loaded
		return nil
	}))
	require.Len(t, records, EvidenceConflictMaxResults)
	actualIDs := make([]string, 0, len(records))
	for _, record := range records {
		actualIDs = append(actualIDs, record.ConflictID)
	}
	require.Equal(t, expectedIDs, actualIDs)
}

func TestEvidenceConflictConcurrentCreationSerializesSameOwnerAndDifferentOwner(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-conflict-concurrent", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-conflict-concurrent-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "evidence-conflict-concurrent-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "evidence-conflict-concurrent-owner-b")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)
	if sqlDB, err := appDB.DB(); err == nil {
		sqlDB.SetMaxOpenConns(8)
		sqlDB.SetMaxIdleConns(8)
	}

	for _, testCase := range []struct {
		name   string
		owners []string
		label  string
	}{
		{name: "same owner", owners: []string{ownerA, ownerA}, label: "same-owner"},
		{name: "different same-team owners", owners: []string{ownerA, ownerB}, label: "different-owners"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			sharedContent := "Concurrent cited evidence shared " + testCase.label + "."
			seedFirst := duplicateRememberInput(teamID, ownerA, "evidence-conflict-concurrent-seed-first-"+testCase.label, sharedContent, false)
			seedFirst.SpaceID, seedFirst.SpaceGeneration = spaceID, generation
			commitDuplicateFixture(t, ctx, repo, seedFirst)
			seedSecond := duplicateRememberInput(teamID, ownerA, "evidence-conflict-concurrent-seed-second-"+testCase.label, "Concurrent opposing evidence "+testCase.label+".", false)
			seedSecond.SpaceID, seedSecond.SpaceGeneration = spaceID, generation
			commitDuplicateFixture(t, ctx, repo, seedSecond)

			inputs := make([]SynchronousRememberCommitInput, 0, len(testCase.owners))
			plans := make([]*InlineEmbeddingPlan, 0, len(testCase.owners))
			for index, ownerID := range testCase.owners {
				input := citedEvidenceRememberInput(teamID, ownerID, fmt.Sprintf("evidence-conflict-concurrent-%s-%d", testCase.label, index), sharedContent, "Concurrent opposing evidence "+testCase.label+".", spaceID, generation)
				input.DuplicateResolutions = []RememberDuplicateResolution{
					{EvidenceIndex: 0, EvidenceID: "evidence:0", InputFragmentID: input.Evidence[0].FragmentID, Disposition: "reuse", CandidateFragmentID: seedFirst.Evidence[0].FragmentID, CandidateOwnerID: ownerA},
					{EvidenceIndex: 1, EvidenceID: "evidence:1", InputFragmentID: input.Evidence[1].FragmentID, Disposition: "reuse", CandidateFragmentID: seedSecond.Evidence[0].FragmentID, CandidateOwnerID: ownerA},
				}
				plan, err := repo.PlanRememberEmbeddings(ctx, input)
				require.NoError(t, err)
				inputs = append(inputs, input)
				plans = append(plans, plan)
			}

			start := make(chan struct{})
			errs := make(chan error, len(inputs))
			var group sync.WaitGroup
			for index := range inputs {
				index := index
				group.Add(1)
				go func() {
					defer group.Done()
					<-start
					_, err := repo.CommitRememberWithEmbeddings(ctx, inputs[index], rememberTestEmbeddings(plans[index], false))
					errs <- err
				}()
			}
			close(start)
			group.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}

			firstPositionKey := evidenceConflictPositionKey(resolvedEvidenceConflictCitation{
				CanonicalEvidenceID: seedFirst.Evidence[0].FragmentID,
				ContentHash:         seedFirst.Evidence[0].ContentHash,
			}, 0, 5)
			secondPositionKey := evidenceConflictPositionKey(resolvedEvidenceConflictCitation{
				CanonicalEvidenceID: seedSecond.Evidence[0].FragmentID,
				ContentHash:         seedSecond.Evidence[0].ContentHash,
			}, 0, 5)
			caseKey := evidenceConflictCaseKey(teamID, spaceID, generation, []string{firstPositionKey, secondPositionKey})
			var caseID string
			require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
				return tx.Raw(`
					SELECT conflict_id::text
					FROM evidence_conflict_cases
					WHERE team_id = ?::uuid AND case_key = ?
				`, teamID, caseKey).Row().Scan(&caseID)
			}))
			require.NotEmpty(t, caseID)
			caseCount := duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_conflict_cases WHERE team_id = ?::uuid AND case_key = ?`, teamID, caseKey)
			require.EqualValues(t, 1, caseCount)
			require.EqualValues(t, 2, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_conflict_positions WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, teamID, caseID))
			require.EqualValues(t, 2, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_conflict_events WHERE team_id = ?::uuid AND conflict_id = ?::uuid`, teamID, caseID))
		})
	}
}

func TestEvidenceConflictChangedPositionSetCreatesNewCase(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-conflict-changed-set", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-conflict-changed-set-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-conflict-changed-set-owner")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)

	first := citedEvidenceRememberInput(teamID, ownerID, "evidence-conflict-changed-set-first", "Alice approved the change.", "Alice rejected the change.", spaceID, generation)
	commitCitedEvidenceFixture(t, ctx, repo, first)
	second := citedEvidenceRememberInput(teamID, ownerID, "evidence-conflict-changed-set-second", "Alice approved the change.", "Alice postponed the change.", spaceID, generation)
	commitCitedEvidenceFixture(t, ctx, repo, second)

	var caseCount int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM evidence_conflict_cases WHERE team_id = ?::uuid`, teamID).Row().Scan(&caseCount)
	}))
	require.EqualValues(t, 2, caseCount)
}

func TestEvidenceConflictStaleCandidateRollsBackRemember(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-conflict-stale", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-conflict-stale-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-conflict-stale-owner")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)

	candidate := evidenceOnlyRememberInput(teamID, ownerID, "evidence-conflict-candidate")
	candidate.SpaceID, candidate.SpaceGeneration = spaceID, generation
	commitDuplicateFixture(t, ctx, repo, candidate)
	candidateID := candidate.Evidence[0].FragmentID

	submitted := citedEvidenceRememberInput(teamID, ownerID, "evidence-conflict-stale-submitted", "fresh submitted evidence", "unused second evidence", spaceID, generation)
	submitted.Evidence = submitted.Evidence[:1]
	submitted.EvidenceSecurityResults = submitted.EvidenceSecurityResults[:1]
	submitted.Commit.Items = submitted.Commit.Items[:1]
	submitted.Commit.EvidenceConflictResults[0].Positions = []EvidenceConflictPositionInput{
		{EvidenceID: "evidence:0", Start: 0, End: 5},
		{EvidenceID: candidateID, Start: 0, End: 5},
	}
	submitted.Commit.EvidenceConflictCandidateEvidenceIDs = map[string][]string{"evidence:0": {candidateID}}
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO evidence_quarantines (team_id, fragment_id, ingest_id, owner_profile_id, reason)
			SELECT team_id, fragment_id, ingest_id, owner_profile_id, 'stale cited candidate'
			FROM evidence_fragments WHERE team_id = ?::uuid AND fragment_id = ?::uuid
		`, teamID, candidateID).Error
	}))

	plan, err := repo.PlanRememberEmbeddings(ctx, submitted)
	require.NoError(t, err)
	_, err = repo.CommitRememberWithEmbeddings(ctx, submitted, rememberTestEmbeddings(plan, false))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrEvidenceConflictStaleInput), "err=%v", err)
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, submitted.IngestID))
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_conflict_cases WHERE team_id = ?::uuid`, teamID))
}

func TestEvidenceConflictRecallRequiresEveryPositionToRemainEligible(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-conflict-recall-eligibility", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-conflict-recall-eligibility-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-conflict-recall-eligibility-owner")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	ledger := NewLedgerRepository(appDB, rls)

	input := citedEvidenceRememberInput(teamID, ownerID, "evidence-conflict-recall-eligibility", "Alice approved the change.", "Alice rejected the change.", spaceID, generation)
	commitCitedEvidenceFixture(t, ctx, ledger, input)
	validAt := time.Now().UTC()
	before, err := NewSearchRepository(appDB, rls).RecallEvidence(ctx, RecallEvidenceInput{TeamID: teamID, Query: "approved", Limit: 10})
	require.NoError(t, err)
	require.Len(t, before.EvidenceConflicts, 1)

	_, err = ledger.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, EvidenceIDs: []string{input.Evidence[1].FragmentID},
		Reason: "opposing citation is no longer eligible", IdempotencyKey: "evidence-conflict-recall-eligibility-retract",
		RequestHash: sha256Hex("evidence-conflict-recall-eligibility-retract"),
	})
	require.NoError(t, err)
	after, err := NewSearchRepository(appDB, rls).RecallEvidence(ctx, RecallEvidenceInput{TeamID: teamID, Query: "approved", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, after.EvidenceConflicts)
	historical, err := NewSearchRepository(appDB, rls).RecallEvidence(ctx, RecallEvidenceInput{TeamID: teamID, Query: "approved", ValidAt: &validAt, Limit: 10})
	require.NoError(t, err)
	require.Len(t, historical.EvidenceConflicts, 1, "valid_at before the retraction should retain the cited conflict")
}

func TestEvidenceConflictAllowsExplicitKnownCitationWithoutCandidateAssociation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-conflict-known", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-conflict-known-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-conflict-known-owner")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	ledger := NewLedgerRepository(appDB, rls)

	known := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "evidence-conflict-known-source", "Known evidence is an explicit opposing citation.")
	knownLoaded, err := NewSemanticRepository(appDB, rls).ListSubmissionAssessmentKnownEvidence(ctx, SubmissionAssessmentKnownEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: spaceID, EvidenceIDs: []string{known.Evidence[0].FragmentID},
	})
	require.NoError(t, err)
	require.Len(t, knownLoaded.Evidence, 1)

	submitted := citedEvidenceRememberInput(teamID, ownerID, "evidence-conflict-known-submitted", "Submitted evidence opposes the known citation.", "unused submitted evidence", spaceID, generation)
	submitted.Evidence = submitted.Evidence[:1]
	submitted.EvidenceSecurityResults = submitted.EvidenceSecurityResults[:1]
	submitted.Commit.Items = submitted.Commit.Items[:1]
	submitted.Commit.KnownEvidenceSnapshot = knownLoaded.Evidence
	submitted.Commit.EvidenceConflictResults[0].Positions = []EvidenceConflictPositionInput{
		{EvidenceID: "evidence:0", Start: 0, End: 9},
		{EvidenceID: knownLoaded.Evidence[0].EvidenceID, Start: 0, End: 4},
	}
	submitted.Commit.EvidenceConflictCandidateEvidenceIDs = nil
	commitCitedEvidenceFixture(t, ctx, ledger, submitted)

	var positionCount int
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)::int
			FROM evidence_conflict_positions
			WHERE team_id = ?::uuid AND canonical_evidence_id = ?::uuid
		`, teamID, knownLoaded.Evidence[0].EvidenceID).Row().Scan(&positionCount)
	}))
	require.Equal(t, 1, positionCount)
}

func conflictIDForTest(t *testing.T, db *gorm.DB, rls rLSHelper, teamID string) string {
	t.Helper()
	var conflictID string
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT conflict_id::text FROM evidence_conflict_cases WHERE team_id = ?::uuid ORDER BY created_at LIMIT 1`, teamID).Row().Scan(&conflictID)
	}))
	return conflictID
}
