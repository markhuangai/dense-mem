//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	traceapp "github.com/markhuangai/dense-mem/internal/trace"
)

func TestTraceVerificationPreservesNullableLegacyVerdicts(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "trace-nullable-verdict-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "trace-nullable-verdict-owner")
	semantic := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})

	subject, err := semantic.CreateEntity(ctx, knowledgepostgres.CreateEntityInput{
		TeamID: teamID, OwnerProfileID: ownerID, EntityKind: "project", CanonicalName: "Nullable subject",
	})
	require.NoError(t, err)
	object, err := semantic.CreateEntity(ctx, knowledgepostgres.CreateEntityInput{
		TeamID: teamID, OwnerProfileID: ownerID, EntityKind: "project", CanonicalName: "Nullable object",
	})
	require.NoError(t, err)
	ingest, err := semantic.CreateIngestForTest(ctx, knowledgepostgres.CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "trace-nullable-verdict-ingest", RequestHash: "trace-nullable-verdict-ingest",
		Evidence: []knowledgepostgres.EvidenceInput{{Content: "Nullable subject uses nullable object."}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Evidence, 1)
	decision, err := semantic.ApplyRelationshipDecision(ctx, knowledgepostgres.ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", PredicateVersion: 1,
		ObjectEntityID: object.EntityID, Polarity: "+", EvidenceVerdict: "entailed",
		Support: &knowledgepostgres.EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "trace-nullable-verdict-source",
			SpanStart: 0, SpanEnd: len("Nullable subject uses nullable object."),
			Quote: "Nullable subject uses nullable object.", Authority: "primary",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, decision.Relationship)
	require.NotEmpty(t, decision.ObservationID)

	for _, verdict := range []any{"contradicted", "insufficient", nil} {
		require.NoError(t, insertTraceVerificationFixture(ctx, adminDB, rls, teamID, ownerID, decision.ObservationID, verdict))
	}

	store := newTraceStoreForTest(appDB, rls)
	service := traceapp.NewSemantic(store)
	actor := requestctx.Actor{TeamID: uuid.MustParse(teamID), OwnerID: uuid.MustParse(ownerID), Grants: []string{"read"}}
	traceCtx := requestctx.WithActor(ctx, actor)
	traceResult, err := service.Trace(traceCtx, "", traceapp.TraceRequest{RelationshipID: decision.Relationship.RelationshipID})
	require.NoError(t, err)
	require.NotNil(t, traceResult)
	require.NotNil(t, traceResult.Semantic)
	require.Len(t, traceResult.Semantic.VerificationEvents, 4)

	seen := map[string]bool{}
	for _, event := range traceResult.Semantic.VerificationEvents {
		if event.Model == "fixture-assessor" {
			require.Equal(t, "legacy trace fixture", event.Rationale)
			require.Equal(t, "fixture-response-hash", event.ResponseHash)
			require.Equal(t, map[string]any{"fixture": "nullable-verdict"}, event.Metadata)
			require.NotNil(t, event.Confidence)
			require.InDelta(t, 0.75, *event.Confidence, 0.0001)
		} else {
			require.Empty(t, event.Rationale)
			require.Empty(t, event.Model)
			require.Empty(t, event.ResponseHash)
			require.Empty(t, event.Metadata)
			require.Nil(t, event.Confidence)
		}
		if event.EvidenceVerdict == nil {
			seen["null"] = true
		} else {
			seen[*event.EvidenceVerdict] = true
		}
	}
	for _, verdict := range []string{"null", "entailed", "contradicted", "insufficient"} {
		require.True(t, seen[verdict], "trace service verdict %q", verdict)
	}

	active, err := registry.BuildActive(registry.Dependencies{TraceBindings: registry.TraceBindings{Service: service}})
	require.NoError(t, err)
	traceTool, ok := active.Get(registry.ToolTraceMemory)
	require.True(t, ok)
	output, err := traceTool.Invoke(traceCtx, "", map[string]any{"relationship_id": decision.Relationship.RelationshipID})
	require.NoError(t, err)
	require.NoError(t, registry.ValidateInput(registry.Tool{InputSchema: traceTool.OutputSchema}, output))

	verificationOutput, ok := output["verification_events"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, verificationOutput, 4)
	outputSeen := map[string]bool{}
	for _, event := range verificationOutput {
		verdict, exists := event["evidence_verdict"]
		require.True(t, exists, "verification output must retain the required verdict key")
		if confidence, exists := event["confidence"]; exists {
			require.Equal(t, "legacy trace fixture", event["rationale"])
			require.InDelta(t, 0.75, confidence, 0.0001)
		} else {
			require.NotContains(t, event, "rationale")
		}
		if verdict == nil {
			outputSeen["null"] = true
			continue
		}
		value, ok := verdict.(string)
		require.True(t, ok)
		outputSeen[value] = true
	}
	require.Equal(t, seen, outputSeen)

	wire, err := json.Marshal(output)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(wire, &decoded))
	decodedEvents, ok := decoded["verification_events"].([]any)
	require.True(t, ok)
	var nullWireVerdict bool
	for _, raw := range decodedEvents {
		event, ok := raw.(map[string]any)
		require.True(t, ok)
		value, exists := event["evidence_verdict"]
		require.True(t, exists)
		if value == nil {
			nullWireVerdict = true
		}
	}
	require.True(t, nullWireVerdict, "serialized trace must contain an explicit null verdict")
}

func insertTraceVerificationFixture(ctx context.Context, db *gorm.DB, rls interface {
	WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
}, teamID, ownerID, observationID string, verdict any) error {
	return rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO verification_events (
				team_id, observation_id, owner_profile_id, evidence_verdict,
				confidence, rationale, model, response_hash, metadata, space_id
			)
			SELECT ?::uuid, observation_id, ?::uuid, ?, 0.75,
			       'legacy trace fixture', 'fixture-assessor', 'fixture-response-hash',
			       '{"fixture":"nullable-verdict"}'::jsonb, space_id
			FROM relationship_observations
			WHERE team_id = ?::uuid AND observation_id = ?::uuid
		`, teamID, ownerID, verdict, teamID, observationID).Error
	})
}
