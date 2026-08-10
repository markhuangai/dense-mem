package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestReadTelemetryLifecycleIsolatesSystemTeamAndProfileScopes(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "telemetry-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "telemetry-owner-a")
	ownerA2 := createLedgerProfile(t, adminDB, rls, teamA, "telemetry-owner-a2")
	teamB := createLedgerTeam(t, adminDB, rls, "telemetry-team-b")
	ownerB := createLedgerProfile(t, adminDB, rls, teamB, "telemetry-owner-b")

	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	createTelemetryRelationship(t, ctx, semantic, ledger, teamA, ownerA, "telemetry-a")
	createTelemetryRelationship(t, ctx, semantic, ledger, teamB, ownerB, "telemetry-b")

	reader := ledger
	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Minute)
	teamAUUID := uuid.MustParse(teamA)
	ownerAUUID := uuid.MustParse(ownerA)
	ownerA2UUID := uuid.MustParse(ownerA2)

	system, err := reader.ReadTelemetryLifecycle(ctx, TelemetryLifecycleFilter{}, from, to)
	require.NoError(t, err)
	require.Equal(t, 2.0, system.Transitions["active"])
	require.Equal(t, 2.0, system.Current["active"])

	teamSnapshot, err := reader.ReadTelemetryLifecycle(ctx, TelemetryLifecycleFilter{TeamID: &teamAUUID}, from, to)
	require.NoError(t, err)
	require.Equal(t, 1.0, teamSnapshot.Transitions["active"])
	require.Equal(t, 1.0, teamSnapshot.Current["active"])

	profileSnapshot, err := reader.ReadTelemetryLifecycle(ctx, TelemetryLifecycleFilter{TeamID: &teamAUUID, ProfileID: &ownerAUUID}, from, to)
	require.NoError(t, err)
	require.Equal(t, 1.0, profileSnapshot.Transitions["active"])
	require.Equal(t, 1.0, profileSnapshot.Current["active"])

	otherOwnerSnapshot, err := reader.ReadTelemetryLifecycle(ctx, TelemetryLifecycleFilter{TeamID: &teamAUUID, ProfileID: &ownerA2UUID}, from, to)
	require.NoError(t, err)
	require.Empty(t, otherOwnerSnapshot.Transitions)
	require.Empty(t, otherOwnerSnapshot.Current)
}

func createTelemetryRelationship(t *testing.T, ctx context.Context, semantic *SemanticRepositoryImpl, ledger *LedgerRepositoryImpl, teamID, ownerID, key string) {
	t.Helper()
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", key+" subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", key+" object")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, key+"-ingest", key+" subject uses "+key+" object")
	result := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", PredicateVersion: 1,
		ObjectEntityID: object.EntityID, EvidenceVerdict: "entailed",
		Support: &EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: key,
			SpanStart: 0, SpanEnd: len(key + " subject uses " + key + " object"), Authority: "primary",
		},
	})
	require.NotNil(t, result.Relationship)
}
