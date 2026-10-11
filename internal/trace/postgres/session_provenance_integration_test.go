//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	sessionservice "github.com/markhuangai/dense-mem/internal/session/service"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTraceSessionProvenanceRequiresAuthenticatedPrivateOwner(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	for _, kind := range []domain.MemorySpaceKind{domain.MemorySpaceProfilePrivate, domain.MemorySpaceCredentialPrivate} {
		t.Run(string(kind), func(t *testing.T) {
			team := createLedgerTeam(t, admin, rls, "trace-session-"+string(kind))
			owner := createLedgerProfile(t, admin, rls, team, "owner-A")
			spaces := privacy.NewMemorySpaceRepository(app, rls)
			var space *domain.MemorySpace
			var err error
			if kind == domain.MemorySpaceProfilePrivate {
				space, err = spaces.EnsureProfilePrivate(context.Background(), uuid.MustParse(team), uuid.MustParse(owner))
			} else {
				space, err = spaces.EnsureCredentialPrivate(context.Background(), uuid.MustParse(team), uuid.New())
			}
			require.NoError(t, err)
			var generation int64
			require.NoError(t, rls.WithSystemTx(context.Background(), admin, func(tx *gorm.DB) error {
				return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?`, space.ID).Row().Scan(&generation)
			}))
			actor := requestctx.Actor{TeamID: uuid.MustParse(team), OwnerID: uuid.MustParse(owner), Grants: []string{"read", "write"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: space.ID, Kind: kind, Generation: generation}}}
			ctx := requestctx.WithActor(context.Background(), actor)
			request := session.Request{IdempotencyKey: "trace-session", Framework: "generic", AppName: "notes", UserID: "external-user", SessionID: "conversation", Events: []session.Event{{EventID: "one", Text: "Ari uses Go. café 🧭"}}}
			hash, err := sessionservice.RequestHash(request)
			require.NoError(t, err)
			windows, err := sessionservice.BuildWindows(request, "o200k_base")
			require.NoError(t, err)
			semantic := knowledge.NewStore(app, rls, knowledge.ConflictRuntimeConfig{})
			staged, err := semantic.StageSession(ctx, session.Intake{Scope: session.Scope{TeamID: team, OwnerProfileID: owner, SpaceID: space.ID.String(), SpaceGeneration: generation}, Request: request, RequestHash: hash, Windows: windows, ExtractionVersion: session.ExtractionVersion})
			require.NoError(t, err)
			provenance := map[string]any{"framework": request.Framework, "app_name": request.AppName, "user_id": request.UserID, "session_id": request.SessionID, "event_id": "one", "event_index": 0, "span_start": 0, "span_end": len([]rune(request.Events[0].Text))}
			store := newTraceStoreForTest(app, rls)
			for _, registered := range []bool{true, false} {
				ingestID := staged.ID
				if !registered {
					ingestID = uuid.NewString()
				}
				ingest, err := semantic.CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: team, OwnerProfileID: owner, IngestID: ingestID, SpaceID: space.ID.String(), SpaceGeneration: generation, IdempotencyKey: ingestID, RequestHash: ingestID, Evidence: []knowledge.EvidenceInput{{Content: request.Events[0].Text, ForceInsert: true, Metadata: map[string]any{"session": provenance}}}})
				require.NoError(t, err)
				occurrenceID := ingest.Evidence[0].FragmentID
				input := traceExecutionInput{Input: TraceRelationshipInput{TeamID: team, MaxFragmentContentRunes: 2000}, spaceID: space.ID.String()}
				require.NoError(t, store.withTeamTx(ctx, team, func(tx *gorm.DB) error {
					for _, occurrence := range []bool{false, true} {
						var rows []TraceEvidenceFragment
						var err error
						if occurrence {
							rows, err = loadTraceEvidenceOccurrences(ctx, tx, input, []string{occurrenceID})
						} else {
							rows, err = loadTraceEvidenceFragments(ctx, tx, input, []string{ingest.Evidence[0].FragmentID})
						}
						require.NoError(t, err)
						require.Len(t, rows, 1)
						if !registered {
							require.Nil(t, rows[0].Session, "client metadata cannot impersonate session intake")
							continue
						}
						require.NotNil(t, rows[0].Session)
						body, err := json.Marshal(rows[0].Session)
						require.NoError(t, err)
						expected, err := json.Marshal(provenance)
						require.NoError(t, err)
						require.JSONEq(t, string(expected), string(body))
					}
					return nil
				}))
				subjectID, objectID := uuid.NewString(), uuid.NewString()
				require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
					return tx.Exec(`INSERT INTO entity_records (team_id,entity_id,entity_kind,space_id,space_generation)
						VALUES (?::uuid,?::uuid,'person',?::uuid,?), (?::uuid,?::uuid,'product',?::uuid,?)`,
						team, subjectID, space.ID, generation, team, objectID, space.ID, generation).Error
				}))
				decision, err := semantic.ApplyRelationshipDecision(ctx, knowledge.ApplyRelationshipDecisionInput{
					TeamID: team, OwnerProfileID: owner, IngestID: ingestID,
					SubjectEntityID: subjectID, ObjectEntityID: objectID, PredicateKey: "uses", PredicateVersion: 1,
					Polarity: "+", EvidenceVerdict: "entailed", Support: &knowledge.EvidenceSupportInput{
						FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: ingestID,
						SpanStart: 0, SpanEnd: len([]rune(request.Events[0].Text)), Quote: request.Events[0].Text, Authority: "primary",
					},
				})
				require.NoError(t, err)
				traced, err := store.TraceRelationship(ctx, TraceRelationshipInput{TeamID: team, RelationshipID: decision.Relationship.RelationshipID})
				require.NoError(t, err)
				require.Len(t, traced.EvidenceFragments, 1)
				require.Equal(t, registered, traced.EvidenceFragments[0].Session != nil)
				for _, foreignTeam := range []bool{false, true} {
					isolatedTeam := team
					if foreignTeam {
						isolatedTeam = createLedgerTeam(t, admin, rls, "trace-session-C-"+string(kind)+"-"+ingestID)
					}
					isolatedOwner := createLedgerProfile(t, admin, rls, isolatedTeam, "isolated-"+ingestID)
					isolatedCtx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: uuid.MustParse(isolatedTeam), OwnerID: uuid.MustParse(isolatedOwner), Grants: []string{"read"}})
					_, err = store.TraceRelationship(isolatedCtx, TraceRelationshipInput{TeamID: isolatedTeam, RelationshipID: decision.Relationship.RelationshipID})
					require.ErrorIs(t, err, ErrTraceRelationshipNotFound)
				}
			}
		})
	}
}
