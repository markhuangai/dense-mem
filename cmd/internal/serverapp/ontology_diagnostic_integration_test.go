//go:build integration

package serverapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	accesspg "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepg "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontologypg "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationspg "github.com/markhuangai/dense-mem/internal/operations/postgres"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspg "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/stretchr/testify/require"
)

func TestOntologyFailureCapturePersistsAtInfoAndScopesControlReads(t *testing.T) {
	for _, mode := range []string{"invalid_response", "canceled", "success", "repair_success", "repair_exhausted"} {
		t.Run(mode, func(t *testing.T) {
			admin, app, rls, cleanup := setupRememberProcessorIntegrationDB(t)
			defer cleanup()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			teamID, ownerID := uuid.New(), uuid.New()
			require.NoError(t, accesspg.NewTeamRepository(admin, rls).Create(ctx, &domain.Team{ID: teamID, Name: "diagnostic-" + uuid.NewString()}))
			credential := &domain.Credential{ID: ownerID, TeamID: teamID, Name: "diagnostic owner", KeyHash: "synthetic", KeyPrefix: strings.ReplaceAll(ownerID.String(), "-", "")[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}
			require.NoError(t, accesspg.NewCredentialRepository(admin, rls, nil).CreateCredential(ctx, credential))
			store := knowledgepg.NewStore(app, rls, knowledge.ConflictRuntimeConfig{})
			var sharedSpaceID string
			var generation int64
			require.NoError(t, admin.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, teamID).Row().Scan(&sharedSpaceID, &generation))
			content := "Diagnostic source canary uses PostgreSQL."
			contentHash := sha256.Sum256([]byte(content))
			_, err := store.CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: teamID.String(), OwnerProfileID: ownerID.String(),
				SpaceID: sharedSpaceID, SpaceGeneration: generation, IdempotencyKey: uuid.NewString(), RequestHash: hex.EncodeToString(contentHash[:]),
				Evidence: []knowledge.EvidenceInput{{FragmentID: uuid.NewString(), Content: content, ForceInsert: true}}})
			require.NoError(t, err)
			configuration := settings.NewAppConfigService(settingspg.NewAppConfigRepository(app, rls), nil)
			_, err = configuration.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "true", domain.AppConfigOntologyModel: "fixture-model", domain.AppConfigOntologyContextWindow: "300000"}, "control", "", "")
			require.NoError(t, err)
			const secret = "known-provider-secret"
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var envelope struct {
					Messages []modelprovider.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil || len(envelope.Messages) < 2 {
					http.Error(w, "invalid fixture input", 400)
					return
				}
				if mode == "canceled" {
					cancel()
					return
				}
				if mode == "success" || strings.HasPrefix(mode, "repair_") {
					var input assessment.Request
					if err := json.Unmarshal([]byte(envelope.Messages[1].Content), &input); err != nil {
						http.Error(w, "invalid fixture assessment", 400)
						return
					}
					response := pairedOrganizationResponse(input)
					if strings.HasPrefix(mode, "repair_") {
						if calls.Load() > 1 {
							var feedback struct {
								ValidationErrors []assessor.SemanticValidationError `json:"validation_errors"`
							}
							require.NoError(t, json.Unmarshal([]byte(envelope.Messages[len(envelope.Messages)-1].Content), &feedback))
							require.Len(t, feedback.ValidationErrors, 2)
							require.Contains(t, feedback.ValidationErrors[0].Field, "items[0]")
							detail := (&assessor.SemanticAssessmentResponseValidationError{Errors: feedback.ValidationErrors}).Error()
							require.Contains(t, detail, input.Items[0].Ref)
							require.Contains(t, detail, "expected topic")
							require.Contains(t, detail, `selects kind "entity_class"`)
							require.Contains(t, detail, "at most 128 bytes")
							for _, issue := range feedback.ValidationErrors {
								require.Empty(t, issue.Code)
							}
						}
						if mode == "repair_exhausted" || calls.Load() < 3 {
							require.Len(t, response.Definitions, 1)
							response.Definitions[0].Kind = "entity_class"
							response.Definitions[0].BaseEntityKind = "project"
							response.Items[0].Reason = strings.Repeat("x", 129)
						}
					}
					body, err := json.Marshal(response)
					if err != nil {
						http.Error(w, "invalid fixture response", 500)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(body)}}}})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"canary":"malformed assessment ` + secret + `"}`}}}})
			}))
			defer upstream.Close()
			protector := observability.NewCredentialProtector(secret)
			logger := observability.NewWithProtector(slog.LevelInfo, protector)
			logs := operations.NewOperationLogService(operationspg.NewOperationLogRepository(app, rls), nil)
			require.NoError(t, logger.AttachSink(logs))
			client := observability.DiagnosticHTTPClient(upstream.Client(), rootDiagnosticLogger(logger), protector)
			provider := assessorprovider.NewOpenAIAssessor(&config.Config{AIVerifierAPIURL: upstream.URL, AIVerifierAPIKey: secret, AIVerifierModel: "fixture-model"}, client)
			maintenance := buildOntologyMaintenance(ontologypg.NewStore(app, rls), configuration, provider, assessor.DefaultSemanticAssessmentLimits(), "fixture-model", time.Minute, nil, rootSlogLogger(logger), ontologyDiagnosticCapture(logs, protector))
			var failure *domain.OperationLog
			for range 12 {
				_, runErr := maintenance.RunTurn(ctx)
				if mode == "success" || mode == "repair_success" {
					require.NoError(t, runErr)
				}
				page, err := logs.ListOperationLogs(context.Background(), domain.OperationLogFilter{Event: "ontology_failure_capture", TeamID: &teamID})
				require.NoError(t, err)
				if len(page.Items) > 0 {
					failure = &page.Items[0]
					break
				}
			}
			require.Positive(t, calls.Load())
			if mode == "success" || mode == "repair_success" {
				require.Nil(t, failure, "successful exchanges must not enter the failure panel")
				if mode == "repair_success" {
					require.Equal(t, int32(3), calls.Load())
				}
				return
			}
			require.NotNil(t, failure, "native maintenance must retain the failed provider exchanges")
			if mode == "invalid_response" || mode == "repair_exhausted" {
				require.Equal(t, "provider_response_invalid", failure.Attrs["failure_code"])
				attempts, err := logs.ListOperationLogs(context.Background(), domain.OperationLogFilter{Event: "ontology maintenance assessment attempt", TeamID: &teamID})
				require.NoError(t, err)
				require.Len(t, attempts.Items, 3)
				for _, attempt := range attempts.Items {
					measured, ok := attempt.Attrs["diagnostics"].(map[string]any)
					require.True(t, ok, "provider durations must remain visible after credential protection")
					require.Contains(t, measured, "http_ms")
					validation, ok := attempt.Attrs["validation"].(map[string]any)
					require.True(t, ok, "the closed validation reason must remain visible after credential protection")
					stage := "decode"
					if mode == "repair_exhausted" {
						stage = "validate"
						require.Equal(t, "item_reason_limit", validation["code"])
					}
					require.Equal(t, stage, validation["stage"])
					require.NotEmpty(t, validation["code"])
				}
			} else {
				require.NotEmpty(t, failure.Attrs["failure_code"])
			}
			filter := domain.OperationLogFilter{Event: "ontology_failure_payload", TeamID: &teamID, ReferenceType: "ontology_run", ReferenceID: failure.Attrs["reference_id"].(string), AttemptID: failure.Attrs["attempt_id"].(string), Limit: 500}
			page, err := logs.ListOperationLogs(context.Background(), filter)
			require.NoError(t, err)
			require.EqualValues(t, failure.Attrs["record_count"], page.Total)
			minimumRecords := int64(6)
			if mode == "canceled" {
				minimumRecords = 2
			}
			require.GreaterOrEqual(t, page.Total, minimumRecords)
			encoded, err := json.Marshal(page.Items)
			require.NoError(t, err)
			if mode == "invalid_response" {
				require.Contains(t, string(encoded), "malformed assessment")
			}
			if mode == "repair_exhausted" {
				require.Contains(t, string(encoded), "validation_errors")
				require.Contains(t, string(encoded), "expected topic")
				require.Contains(t, string(encoded), "entity_class")
			}
			require.NotContains(t, string(encoded), secret)
			otherTeam := uuid.New()
			filter.TeamID = &otherTeam
			other, err := logs.ListOperationLogs(context.Background(), filter)
			require.NoError(t, err)
			require.Zero(t, other.Total)
			filter.TeamID = &teamID
			filter.AttemptID = uuid.NewString()
			other, err = logs.ListOperationLogs(context.Background(), filter)
			require.NoError(t, err)
			require.Zero(t, other.Total)
		})
	}
}
