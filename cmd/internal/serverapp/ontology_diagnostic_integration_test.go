//go:build integration

package serverapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypg "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationspg "github.com/markhuangai/dense-mem/internal/operations/postgres"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspg "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/stretchr/testify/require"
)

func TestOntologyFailureCapturePersistsAtInfoAndScopesControlReads(t *testing.T) {
	for _, mode := range []string{"invalid_response", "canceled", "success", "repair_success", "repair_exhausted", "unused_exhausted", "stale_source"} {
		t.Run(mode, func(t *testing.T) {
			admin, app, rls, cleanup := setupRememberProcessorIntegrationDB(t)
			defer cleanup()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			teamID, ownerID := uuid.New(), uuid.New()
			require.NoError(t, accesspg.NewTeamRepository(admin, rls).Create(ctx, &domain.Team{ID: teamID, Name: "diagnostic-" + uuid.NewString()}))
			credential := &domain.Credential{ID: ownerID, TeamID: teamID, Name: "diagnostic owner", KeyHash: "synthetic", KeyPrefix: strings.ReplaceAll(ownerID.String(), "-", "")[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}
			require.NoError(t, accesspg.NewCredentialRepository(admin, rls, nil).CreateCredential(ctx, credential))
			store := knowledgepg.NewStore(app, rls)
			var sharedSpaceID string
			var generation int64
			require.NoError(t, admin.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, teamID).Row().Scan(&sharedSpaceID, &generation))
			content := "Diagnostic source canary uses PostgreSQL."
			contentHash := sha256.Sum256([]byte(content))
			evidenceID := uuid.NewString()
			_, err := store.CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: teamID.String(), OwnerProfileID: ownerID.String(),
				SpaceID: sharedSpaceID, SpaceGeneration: generation, IdempotencyKey: uuid.NewString(), RequestHash: hex.EncodeToString(contentHash[:]),
				Evidence: []knowledge.EvidenceInput{{FragmentID: evidenceID, Content: content, ForceInsert: true}}})
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
				if mode == "success" || mode == "stale_source" || mode == "unused_exhausted" || strings.HasPrefix(mode, "repair_") {
					var input assessment.Request
					if err := json.Unmarshal([]byte(envelope.Messages[1].Content), &input); err != nil {
						http.Error(w, "invalid fixture assessment", 400)
						return
					}
					response := pairedOrganizationResponse(input)
					if mode == "unused_exhausted" {
						if calls.Load() > 1 {
							var feedback struct {
								ValidationErrors []assessor.SemanticValidationError `json:"validation_errors"`
							}
							require.NoError(t, json.Unmarshal([]byte(envelope.Messages[len(envelope.Messages)-1].Content), &feedback))
							require.Len(t, feedback.ValidationErrors, 1)
							require.Contains(t, feedback.ValidationErrors[0].Message, "has no source assignment")
						}
						ref := fmt.Sprintf("unused_%d", calls.Load())
						response.Definitions = append(response.Definitions, assessment.Definition{Ref: ref, Kind: contract.Topic,
							Key: ref, Label: "Unused definition", Description: "A new unused proposal on each complete regeneration.", Aliases: []string{}})
					}
					if mode == "stale_source" && calls.Load() == 1 {
						_, err := store.RetractEvidence(context.Background(), knowledge.RetractEvidenceInput{TeamID: teamID.String(), OwnerProfileID: ownerID.String(),
							EvidenceIDs: []string{evidenceID}, Reason: "withdrawn during provider exchange", IdempotencyKey: uuid.NewString(), RequestHash: hex.EncodeToString(contentHash[:])})
						require.NoError(t, err)
					}
					if strings.HasPrefix(mode, "repair_") {
						if calls.Load() > 1 {
							var feedback struct {
								ValidationErrors []assessor.SemanticValidationError `json:"validation_errors"`
							}
							require.NoError(t, json.Unmarshal([]byte(envelope.Messages[len(envelope.Messages)-1].Content), &feedback))
							require.Len(t, feedback.ValidationErrors, 3)
							require.Equal(t, "definitions[0]", feedback.ValidationErrors[0].Field)
							require.Equal(t, "items[0].definition_ref", feedback.ValidationErrors[1].Field)
							require.Equal(t, "items[0].reason", feedback.ValidationErrors[2].Field)
							detail := (&assessor.SemanticAssessmentResponseValidationError{Errors: feedback.ValidationErrors}).Error()
							require.Contains(t, detail, input.Items[0].Ref)
							require.Contains(t, detail, "requires an Evidence topic")
							require.Contains(t, detail, `received kind "entity_class"`)
							require.Contains(t, detail, "at most 128 bytes")
							for _, issue := range feedback.ValidationErrors {
								require.Empty(t, issue.Code)
							}
						}
						if mode == "repair_exhausted" || calls.Load() < 3 {
							require.Len(t, response.Definitions, 1)
							response.Definitions[0].Kind = "entity_class"
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
			level := slog.LevelInfo
			if mode == "stale_source" || mode == "repair_exhausted" || mode == "unused_exhausted" {
				level = slog.LevelDebug
			}
			logger := observability.NewWithProtector(level, protector)
			logs := operations.NewOperationLogService(operationspg.NewOperationLogRepository(app, rls), nil)
			require.NoError(t, logger.AttachSink(logs))
			client := observability.DiagnosticHTTPClient(upstream.Client(), rootDiagnosticLogger(logger), protector)
			provider := assessorprovider.NewOpenAIAssessor(&config.Config{AIVerifierAPIURL: upstream.URL, AIVerifierAPIKey: secret, AIVerifierModel: "fixture-model"}, client)
			maintenance := buildOntologyMaintenance(ontologypg.NewStore(app, rls), configuration, provider, assessor.DefaultSemanticAssessmentLimits(), "fixture-model", time.Minute, nil, rootSlogLogger(logger), ontologyDiagnosticCapture(logs, protector, rootDiagnosticLogger(logger)))
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
			if mode == "stale_source" {
				require.Equal(t, "stale_input", failure.Attrs["failure_code"])
				warnings, err := logs.ListOperationLogs(context.Background(), domain.OperationLogFilter{Event: "ontology_freshness_rejected", TeamID: &teamID})
				require.NoError(t, err)
				require.Len(t, warnings.Items, 1)
				require.Equal(t, "WARN", warnings.Items[0].Severity)
				require.Equal(t, "source_fingerprint", warnings.Items[0].Attrs["check"])
				comparisonFilter := domain.OperationLogFilter{Event: "ontology_freshness_payload", TeamID: &teamID,
					ReferenceType: "ontology_run", ReferenceID: failure.Attrs["reference_id"].(string), AttemptID: failure.Attrs["attempt_id"].(string), Limit: 500}
				comparison, err := logs.ListOperationLogs(context.Background(), comparisonFilter)
				require.NoError(t, err)
				require.EqualValues(t, failure.Attrs["freshness_record_count"], comparison.Total)
				require.Len(t, comparison.Items, 1)
				encoded, err := json.Marshal(comparison.Items[0].Attrs["payload_chunk"])
				require.NoError(t, err)
				var chunk observability.DiagnosticBodyChunk
				require.NoError(t, json.Unmarshal(encoded, &chunk))
				require.Equal(t, "captured", chunk.State)
				require.Equal(t, 1, chunk.Count)
				digest := sha256.Sum256([]byte(chunk.Body))
				require.Equal(t, hex.EncodeToString(digest[:]), chunk.SHA256)
				var rejection contract.FreshnessRejection
				require.NoError(t, json.Unmarshal([]byte(chunk.Body), &rejection))
				require.True(t, rejection.Valid())
				require.Equal(t, evidenceID, rejection.Source.ID)
				require.NotEqual(t, rejection.Expected, rejection.Observed)
				require.NotContains(t, chunk.Body, secret)
				foreign := uuid.New()
				comparisonFilter.TeamID = &foreign
				invisible, err := logs.ListOperationLogs(context.Background(), comparisonFilter)
				require.NoError(t, err)
				require.Zero(t, invisible.Total)
			}
			if mode == "invalid_response" || mode == "repair_exhausted" || mode == "unused_exhausted" {
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
						require.Equal(t, "definition_record", validation["code"])
					}
					if mode == "unused_exhausted" {
						stage = "validate"
						require.Equal(t, "definition_unused", validation["code"])
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
			if mode == "canceled" || mode == "stale_source" {
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
			if mode == "unused_exhausted" {
				require.Contains(t, string(encoded), "validation_errors")
				require.Contains(t, string(encoded), "has no source assignment")
				require.Contains(t, string(encoded), "unused_3")
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
