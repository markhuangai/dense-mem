//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/stretchr/testify/require"
)

func TestOntologyMaintenanceAttemptDiagnosticsOperatorLogIsBounded(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			f := newOrganizationFixture(t)
			f.organizationEvidence(t, 0, "Atlas uses PostgreSQL. raw-source-canary", nil)
			settings := maintenanceSettings(t, f)
			var output bytes.Buffer
			service := organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: f.store, Config: settings, DefaultModel: "fixture-model", ProviderTimeout: time.Minute,
				Logger: slog.New(slog.NewJSONHandler(&output, nil)), Organizer: func(model string, contextWindowTokens int64, accounting assessment.AttemptAccounting) *organization.Service {
					edit := func(_ assessment.Request, response *assessment.Response) {
						if invalid {
							response.Items[0].DefinitionRef = "synthetic-provider-key"
						}
					}
					organizer, _ := organizationFixtureServiceWithAccounting(t, f, nil, edit, model, maintenanceFixtureLimits(), accounting, int(contextWindowTokens))
					return organizer
				}})
			for range 8 {
				_, err := service.RunTurn(context.Background())
				if output.Len() > 0 {
					if invalid {
						require.ErrorContains(t, err, "provider_response_invalid")
					} else {
						require.NoError(t, err)
					}
					break
				}
				require.NoError(t, err)
			}
			require.NotEmpty(t, output.String())
			require.NotContains(t, output.String(), "raw-source-canary")
			require.NotContains(t, output.String(), "synthetic-provider-key")
			lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
			attemptCount := 1
			if invalid {
				attemptCount = 3
			}
			require.Len(t, lines, attemptCount)
			for _, line := range lines {
				var fields map[string]any
				require.NoError(t, json.Unmarshal(line, &fields))
				require.Equal(t, "ontology maintenance assessment attempt", fields["msg"])
				require.Equal(t, false, fields["reported_usage_available"])
				diagnostics, ok := fields["diagnostics"].(map[string]any)
				require.True(t, ok)
				for _, field := range []string{"queue_ms", "admission_ms", "http_ms", "reconciliation_ms"} {
					require.Contains(t, diagnostics, field)
				}
				if invalid {
					require.Equal(t, map[string]any{"stage": "validate", "code": "item_definition_ref", "field_family": "items"}, fields["validation"])
				} else {
					require.NotContains(t, fields, "validation")
				}
			}
		})
	}
}

func TestOntologyMaintenanceAttemptDiagnosticsPersistenceAndFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"reported", "unknown_usage", "timeout", "canceled", "reconciliation_failure", "decode_invalid", "validate_invalid", "repaired"} {
		t.Run(mode, func(t *testing.T) {
			x := newFinalizationFixture(t)
			f := x.f
			other := maintenanceOtherTeam(t, f)
			before := f.canonicalSnapshot(t)
			var publicationsBefore int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_publications WHERE team_id=?::uuid`, f.team).Row().Scan(&publicationsBefore))
			if mode == "reconciliation_failure" {
				require.NoError(t, f.admin.Exec(`CREATE FUNCTION reject_diagnostic_reconciliation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic reconciliation failure' USING ERRCODE='P0001'; END $$;
				 CREATE TRIGGER reject_diagnostic_reconciliation BEFORE UPDATE OF reconciled_at ON ontology_maintenance_attempts FOR EACH ROW EXECUTE FUNCTION reject_diagnostic_reconciliation()`).Error)
			}
			var calls atomic.Int32
			bodyRead := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				var envelope struct {
					Messages []struct{ Role, Content string }
				}
				if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
					t.Error(err)
					return
				}
				if mode == "timeout" || mode == "canceled" {
					w.Header().Set("Content-Length", "1000")
					_, _ = w.Write([]byte(`{"choices":`))
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				var request assessment.Request
				if len(envelope.Messages) < 2 {
					t.Error("missing request")
					return
				}
				if err := json.Unmarshal([]byte(envelope.Messages[1].Content), &request); err != nil {
					t.Error(err)
					return
				}
				response := organizationFixtureResponse(request, nil)
				if mode == "validate_invalid" || (mode == "repaired" && call == 2) {
					response.Items = []assessment.Decision{}
				}
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Error(err)
					return
				}
				content := string(encoded)
				if mode == "decode_invalid" || (mode == "repaired" && call == 1) {
					content = `{"synthetic-provider-secret raw-source-canary":true}`
				}
				body := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}}
				if mode != "unknown_usage" {
					body["usage"] = map[string]int{"prompt_tokens": 17, "completion_tokens": 9, "total_tokens": 26}
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			client := server.Client()
			client.Timeout = time.Second
			if mode == "timeout" {
				client.Timeout = 2 * time.Second
			}
			if mode == "canceled" {
				transport := client.Transport
				client.Transport = diagnosticRoundTripper(func(request *http.Request) (*http.Response, error) {
					response, err := transport.RoundTrip(request)
					if err == nil {
						response.Body = &diagnosticBodyReadSignal{ReadCloser: response.Body, started: bodyRead}
					}
					return response, err
				})
			}
			limits := maintenanceFixtureLimits()
			transport := assessorprovider.NewOpenAIAssessorWithAssessmentLimits(&config.Config{AIVerifierAPIURL: server.URL, AIVerifierModel: "fixture-model"}, client, limits)
			provider := assessment.NewProviderWithAccounting(transport, "fixture-model", limits, finalizationAccounting{f.store, *x.claim}, int(x.window.Policy.ContextWindowTokens))
			service := organization.NewService(f.store, provider)
			ctx, cancel := context.WithCancel(ontology.WithMaintenanceClaim(context.Background(), *x.claim))
			defer cancel()
			if mode == "canceled" {
				go func() {
					select {
					case <-bodyRead:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			input := ontology.OrganizationInput{OperationKey: "maintenance:" + x.claim.ID, Sources: x.claim.Sources}
			result, err := service.Organize(ctx, f.team, input)
			invalid := mode == "decode_invalid" || mode == "validate_invalid"
			failed := invalid || mode == "timeout" || mode == "canceled" || mode == "reconciliation_failure"
			if failed {
				require.Error(t, err)
				require.Nil(t, result.Publication)
				require.NotEmpty(t, result.FailureCode)
			} else {
				require.NoError(t, err)
			}
			attemptCount := 1
			if invalid || mode == "repaired" {
				attemptCount = 3
			}
			require.Len(t, result.Attempts, attemptCount)
			for i, attempt := range result.Attempts {
				require.True(t, attempt.Validation.Valid())
				if invalid || (mode == "repaired" && i < 2) {
					require.NotNil(t, attempt.Validation)
					require.Equal(t, "response_invalid", attempt.FailureCode)
					if mode == "decode_invalid" || (mode == "repaired" && i == 0) {
						require.Equal(t, &ontology.AssessmentValidation{Stage: "decode", Code: "strict_json", FieldFamily: "response"}, attempt.Validation)
					} else {
						require.Equal(t, "validate", attempt.Validation.Stage)
						require.Equal(t, "item_count_mismatch", attempt.Validation.Code)
						require.Equal(t, "items", attempt.Validation.FieldFamily)
						require.Equal(t, len(x.claim.Sources), *attempt.Validation.ExpectedCount)
						require.Zero(t, *attempt.Validation.ActualCount)
					}
				} else {
					require.Nil(t, attempt.Validation)
				}
			}
			if invalid || mode == "repaired" {
				var count, reconciled, inputTokens, outputTokens, totalTokens int
				require.NoError(t, f.admin.Raw(`SELECT count(*),count(reconciled_at),sum(reported_input),sum(reported_output),sum(reported_total)
				 FROM ontology_maintenance_attempts WHERE batch_id=?::uuid AND assessment_id=?::uuid`, x.claim.ID, result.AssessmentID).
					Row().Scan(&count, &reconciled, &inputTokens, &outputTokens, &totalTokens))
				require.Equal(t, 3, count)
				require.Equal(t, count, reconciled)
				require.Equal(t, 51, inputTokens)
				require.Equal(t, 27, outputTokens)
				require.Equal(t, 78, totalTokens)
				if invalid {
					require.Equal(t, "provider_response_invalid", result.FailureCode)
					var publications int
					require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_publications WHERE team_id=?::uuid`, f.team).Row().Scan(&publications))
					require.Equal(t, publicationsBefore, publications)
				} else {
					require.NotNil(t, result.Publication)
				}
			}
			attempt := result.Attempts[0]
			require.True(t, attempt.Diagnostics.Valid())
			require.NotNil(t, attempt.Diagnostics.QueueMS)
			require.NotNil(t, attempt.Diagnostics.AdmissionMS)
			require.NotNil(t, attempt.Diagnostics.HTTPMS)
			require.NotNil(t, attempt.Diagnostics.ReconciliationMS)
			if mode == "timeout" || mode == "canceled" {
				require.Equal(t, "response_body", attempt.Diagnostics.FailurePhase)
				require.False(t, attempt.ReportedUsageAvailable)
				require.Zero(t, attempt.ReportedTotalTokens)
			}
			if mode == "unknown_usage" {
				require.False(t, attempt.ReportedUsageAvailable)
			}
			if mode == "reported" {
				require.True(t, attempt.ReportedUsageAvailable)
				require.Equal(t, 26, attempt.ReportedTotalTokens)
			}
			if mode == "reconciliation_failure" {
				require.True(t, attempt.Diagnostics.ReconciliationFailed)
				require.Equal(t, "reconciliation", attempt.Diagnostics.FailurePhase)
			}
			var body []byte
			require.NoError(t, f.admin.Raw(`SELECT body FROM ontology_assessments WHERE assessment_id=?::uuid`, result.AssessmentID).Row().Scan(&body))
			var receipt ontology.OrganizationReceipt
			require.NoError(t, json.Unmarshal(body, &receipt))
			require.NoError(t, ontology.ValidateOrganizationReceipt(receipt))
			require.Equal(t, result.Attempts, receipt.Result.Attempts)
			require.NotContains(t, string(body), "synthetic-provider-secret")
			require.NotContains(t, string(body), "raw-source-canary")
			if failed {
				for _, outcome := range receipt.Result.Outcomes {
					require.Equal(t, "failed", outcome.Status)
				}
			}
			stable := context.Background()
			replayed, _, replayErr := f.store.FindOrganization(stable, f.team, input, provider.Identity())
			require.NoError(t, replayErr)
			require.Equal(t, result.Attempts, replayed.Attempts)
			input.Sources = input.Sources[:1]
			_, _, replayErr = f.store.FindOrganization(stable, f.team, input, provider.Identity())
			require.ErrorIs(t, replayErr, ontology.ErrConflict)
			input.Sources = x.claim.Sources
			_, found, foreignErr := f.store.FindOrganization(stable, other.team, input, provider.Identity())
			require.True(t, foreignErr != nil || !found)
			require.EqualValues(t, attemptCount, calls.Load())
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
}

type diagnosticRoundTripper func(*http.Request) (*http.Response, error)

func (f diagnosticRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type diagnosticBodyReadSignal struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (r *diagnosticBodyReadSignal) Read(body []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.ReadCloser.Read(body)
}
