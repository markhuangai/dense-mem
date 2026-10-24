//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
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
	f := newOrganizationFixture(t)
	f.organizationEvidence(t, 0, "Atlas uses PostgreSQL. raw-source-canary", nil)
	settings := maintenanceSettings(t, f)
	var output bytes.Buffer
	service := organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: f.store, Config: settings, DefaultModel: "fixture-model", ProviderTimeout: time.Minute,
		Logger: slog.New(slog.NewJSONHandler(&output, nil)), Organizer: func(model string, contextWindowTokens int64, accounting assessment.AttemptAccounting) *organization.Service {
			organizer, _ := organizationFixtureServiceWithAccounting(t, f, nil, nil, model, maintenanceFixtureLimits(), accounting, int(contextWindowTokens))
			return organizer
		}})
	for range 8 {
		_, err := service.RunTurn(context.Background())
		require.NoError(t, err)
		if output.Len() > 0 {
			break
		}
	}
	require.NotEmpty(t, output.String())
	require.NotContains(t, output.String(), "raw-source-canary")
	require.NotContains(t, output.String(), "synthetic-provider-key")
	var fields map[string]any
	require.NoError(t, json.Unmarshal(bytes.Split(output.Bytes(), []byte("\n"))[0], &fields))
	require.Equal(t, "ontology maintenance assessment attempt", fields["msg"])
	require.Equal(t, false, fields["reported_usage_available"])
	diagnostics, ok := fields["diagnostics"].(map[string]any)
	require.True(t, ok)
	for _, field := range []string{"queue_ms", "admission_ms", "http_ms", "reconciliation_ms"} {
		require.Contains(t, diagnostics, field)
	}
}

func TestOntologyMaintenanceAttemptDiagnosticsPersistenceAndFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"reported", "unknown_usage", "timeout", "canceled", "reconciliation_failure"} {
		t.Run(mode, func(t *testing.T) {
			x := newFinalizationFixture(t)
			f := x.f
			other := maintenanceOtherTeam(t, f)
			before := f.canonicalSnapshot(t)
			if mode == "reconciliation_failure" {
				require.NoError(t, f.admin.Exec(`CREATE FUNCTION reject_diagnostic_reconciliation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic reconciliation failure' USING ERRCODE='P0001'; END $$;
				 CREATE TRIGGER reject_diagnostic_reconciliation BEFORE UPDATE OF reconciled_at ON ontology_maintenance_attempts FOR EACH ROW EXECUTE FUNCTION reject_diagnostic_reconciliation()`).Error)
			}
			var calls atomic.Int32
			bodyRead := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
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
				encoded, err := json.Marshal(organizationFixtureResponse(request, nil))
				if err != nil {
					t.Error(err)
					return
				}
				body := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(encoded)}}}}
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
			failed := mode == "timeout" || mode == "canceled" || mode == "reconciliation_failure"
			if failed {
				require.Error(t, err)
				require.Nil(t, result.Publication)
				require.NotEmpty(t, result.FailureCode)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, result.Attempts, 1)
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
			require.Equal(t, attempt.Diagnostics, receipt.Result.Attempts[0].Diagnostics)
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
			require.EqualValues(t, 1, calls.Load())
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
