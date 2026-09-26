//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSubmissionPredicateRegistrationPreflightCatalogAndIsolation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "registration-preflight-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "registration-preflight-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "registration-preflight-owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "registration-preflight-team-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "registration-preflight-owner-c")
	store := NewStore(appDB, rls, ConflictRuntimeConfig{})

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, definition := range []struct {
			team, key, lifecycle, relationshipKind, cardinality string
			version                                             int
			aliases                                             []string
		}{
			{teamA, "known_key", "active", "state", "many", 1, []string{"known alias"}},
			{teamA, "retired_key", "active", "state", "many", 1, nil},
			{teamA, "retired_key", "retired", "state", "many", 2, nil},
			{teamA, "alias_left", "active", "state", "many", 1, []string{"shared alias"}},
			{teamA, "alias_right", "active", "state", "many", 1, []string{"shared alias"}},
			{teamA, "requested_key", "active", "state", "many", 1, nil},
			{teamA, "other_key", "active", "event", "many", 1, []string{"requested_key"}},
			{teamA, "canonical_key", "active", "state", "many", 1, nil},
			{teamA, "another_key", "active", "event", "many", 1, []string{"Canonical Key"}},
			{teamC, "cross_team_key", "active", "event", "many", 1, nil},
		} {
			if err := tx.Exec(`
				INSERT INTO team_predicate_definitions (
				    team_id, predicate_key, version, aliases, allowed_subject_kinds,
				    allowed_object_kinds, relationship_kind, current_cardinality,
				    lifecycle_state, origin, metadata
				) VALUES (?::uuid, ?, ?, ?::text[], ARRAY['person']::text[],
				          ARRAY['project']::text[], ?, ?, ?, 'built_in', '{}'::jsonb)
			`, definition.team, definition.key, definition.version, pq.Array(append([]string{}, definition.aliases...)),
				definition.relationshipKind, definition.cardinality, definition.lifecycle).Error; err != nil {
				return err
			}
		}
		return nil
	}))

	registration := func(key string) SubmissionPredicateRegistrationInput {
		return SubmissionPredicateRegistrationInput{
			RelationshipRef: key, PredicateKey: key, SubjectKind: "person", ObjectKind: "project",
			RelationshipKind: "state", CurrentCardinality: "many",
		}
	}
	for _, owner := range []string{ownerA, ownerB} {
		issues, err := store.ValidateSubmissionPredicateRegistrations(ctx, SubmissionPredicateRegistrationValidationInput{
			TeamID: teamA, OwnerProfileID: owner,
			Registrations: []SubmissionPredicateRegistrationInput{
				registration("known alias"), registration("requested_key"), registration("Canonical Key"),
			},
		})
		require.NoError(t, err)
		require.Empty(t, issues)
	}

	for _, testCase := range []struct {
		name, key, field string
		modify           func(*SubmissionPredicateRegistrationInput)
	}{
		{"subject kind", "known_key", "subject_kind", func(r *SubmissionPredicateRegistrationInput) { r.SubjectKind = "organization" }},
		{"object kind", "known_key", "object_kind", func(r *SubmissionPredicateRegistrationInput) { r.ObjectKind = "product" }},
		{"relationship kind", "known_key", "relationship_kind", func(r *SubmissionPredicateRegistrationInput) { r.RelationshipKind = "event" }},
		{"cardinality", "known_key", "current_cardinality", func(r *SubmissionPredicateRegistrationInput) { r.CurrentCardinality = "one" }},
		{"inactive", "retired_key", "predicate_key", func(*SubmissionPredicateRegistrationInput) {}},
		{"ambiguous alias", "shared alias", "predicate_key", func(*SubmissionPredicateRegistrationInput) {}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := registration(testCase.key)
			testCase.modify(&candidate)
			issues, err := store.ValidateSubmissionPredicateRegistrations(ctx, SubmissionPredicateRegistrationValidationInput{
				TeamID: teamA, OwnerProfileID: ownerA, Registrations: []SubmissionPredicateRegistrationInput{candidate},
			})
			require.NoError(t, err)
			require.Len(t, issues, 1)
			require.Equal(t, 0, issues[0].RegistrationIndex)
			require.Equal(t, testCase.field, issues[0].Field)
		})
	}

	issues, err := store.ValidateSubmissionPredicateRegistrations(ctx, SubmissionPredicateRegistrationValidationInput{
		TeamID: teamA, OwnerProfileID: ownerA,
		Registrations: []SubmissionPredicateRegistrationInput{registration("cross_team_key")},
	})
	require.NoError(t, err)
	require.Empty(t, issues)
	issues, err = store.ValidateSubmissionPredicateRegistrations(ctx, SubmissionPredicateRegistrationValidationInput{
		TeamID: teamC, OwnerProfileID: ownerC,
		Registrations: []SubmissionPredicateRegistrationInput{registration("cross_team_key")},
	})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Equal(t, "relationship_kind", issues[0].Field)
}

func TestSubmissionPredicateRegistrationPreflightBatchOverlayAndNoWrites(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "registration-preflight-batch")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "registration-preflight-batch-owner")
	store := NewStore(appDB, rls, ConflictRuntimeConfig{})
	count := func(table string) int64 {
		t.Helper()
		var n int64
		require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Table(table).Where("team_id = ?::uuid", teamID).Count(&n).Error
		}))
		return n
	}
	definitionsBefore := count("team_predicate_definitions")
	eventsBefore := count("predicate_registration_events")
	registrations := make([]SubmissionPredicateRegistrationInput, 513)
	for index := range registrations {
		registrations[index] = SubmissionPredicateRegistrationInput{
			RelationshipRef: fmt.Sprintf("batch-%d", index), PredicateKey: fmt.Sprintf("road_map_%d", index), SubjectKind: "person",
			ObjectKind: "project", RelationshipKind: "state", CurrentCardinality: "many",
		}
	}
	registrations[512].PredicateKey = "Road Map 0"
	registrations[512].RelationshipKind = "event"
	issues, err := store.ValidateSubmissionPredicateRegistrations(ctx, SubmissionPredicateRegistrationValidationInput{
		TeamID: teamID, OwnerProfileID: ownerID, Registrations: registrations,
	})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Equal(t, 512, issues[0].RegistrationIndex)
	require.Equal(t, "relationship_kind", issues[0].Field)
	require.Equal(t, definitionsBefore, count("team_predicate_definitions"))
	require.Equal(t, eventsBefore, count("predicate_registration_events"))
}

func TestSubmissionPredicateRegistrationCatalogDriftPreservesPreviewAndCommitFences(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "registration-preflight-drift", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "registration-preflight-drift")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "registration-preflight-drift-owner")
	store := NewStore(appDB, rls, ConflictRuntimeConfig{})
	subject := createSemanticEntity(t, ctx, store, teamID, ownerID, "project", "Dense-Mem")
	object := createSemanticEntity(t, ctx, store, teamID, ownerID, "product", "PostgreSQL")
	insertVersion := func(key string, version int, kind string) {
		t.Helper()
		require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Exec(`
				INSERT INTO team_predicate_definitions (
				    team_id, predicate_key, version, aliases, allowed_subject_kinds,
				    allowed_object_kinds, relationship_kind, current_cardinality,
				    lifecycle_state, origin, metadata
				) VALUES (?::uuid, ?, ?, ARRAY[]::text[], ARRAY['project']::text[],
				          ARRAY['product']::text[], ?, 'many', 'active',
				          'submission_registration', '{}'::jsonb)
			`, teamID, key, version, kind).Error
		}))
	}
	makeInput := func(key string) SynchronousRememberCommitInput {
		input := conflictRememberFixtureInput(teamID, ownerID, subject.EntityID, object.EntityID,
			"Dense-Mem uses PostgreSQL.", "registration-drift-"+key, nil)
		observation := &input.Commit.RelationshipObservations[0].Observation
		observation.OriginalPredicate = key
		observation.PredicateKey = ""
		observation.PredicateVersion = 0
		input.Commit.PredicateRegistrations = []SubmissionPredicateRegistrationInput{{
			RelationshipRef: observation.Ref, PredicateKey: key,
			SubjectKind: "project", ObjectKind: "product", RelationshipKind: "state", CurrentCardinality: "many",
		}}
		return input
	}
	assertNoCanonicalWrites := func(input SynchronousRememberCommitInput) {
		t.Helper()
		for table, count := range rememberPrimitiveCounts(t, ctx, adminDB, rls, teamID, input.IngestID) {
			require.Zero(t, count, "%s must roll back", table)
		}
		var eventCount int64
		require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT count(*) FROM predicate_registration_events
				WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, input.IngestID).Row().Scan(&eventCount)
		}))
		require.Zero(t, eventCount)
	}
	assertPreflightValid := func(input SynchronousRememberCommitInput) {
		t.Helper()
		issues, err := store.ValidateSubmissionPredicateRegistrations(ctx, SubmissionPredicateRegistrationValidationInput{
			TeamID: teamID, OwnerProfileID: ownerID,
			Registrations: input.Commit.PredicateRegistrations,
		})
		require.NoError(t, err)
		require.Empty(t, issues)
	}

	previewInput := makeInput("drift_preview")
	insertVersion("drift_preview", 1, "state")
	assertPreflightValid(previewInput)
	insertVersion("drift_preview", 2, "event")
	_, err := store.PlanRememberEmbeddings(ctx, previewInput)
	require.ErrorIs(t, err, ErrSubmissionPredicateRegistrationHeld)
	assertNoCanonicalWrites(previewInput)

	commitInput := makeInput("drift_commit")
	insertVersion("drift_commit", 1, "state")
	assertPreflightValid(commitInput)
	plan, err := store.PlanRememberEmbeddings(ctx, commitInput)
	require.NoError(t, err)
	insertVersion("drift_commit", 2, "event")
	_, err = store.CommitRememberWithEmbeddings(ctx, commitInput, rememberTestEmbeddings(plan, false))
	require.ErrorIs(t, err, ErrSubmissionPredicateRegistrationHeld)
	assertNoCanonicalWrites(commitInput)
}
