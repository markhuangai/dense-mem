package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestSubmissionDiagnosticProjectionBoundsEveryOperatorField(t *testing.T) {
	payload := map[string]any{
		"failure_stage":               "assessment",
		"failure_class":               "timeout",
		"validation_stage":            "response_contract",
		"validation_field_families":   []any{"response", "response", "unknown_field", "relationship_results.ref"},
		"failure_measurement":         map[string]any{"unit": "candidates", "observed_at_least": float64(123), "limit": json.Number("100")},
		"provider_status":             float64(503),
		"assessor_turns":              json.Number("1001"),
		"assessor_provider_attempted": true,
	}

	projected, ok := projectSubmissionOperatorDiagnosticPayload(payload)
	require.True(t, ok)
	require.Equal(t, "assessment", projected.FailureStage)
	require.Equal(t, "timeout", projected.FailureClass)
	require.Equal(t, "response_contract", projected.ValidationStage)
	require.Equal(t, []string{"response", "relationship_results.ref"}, projected.ValidationFieldFamilies)
	require.Equal(t, 503, projected.ProviderStatus)
	require.Equal(t, 1000, projected.AssessorTurns)
	require.True(t, projected.ProviderAttempted)
	require.Equal(t, "candidates", projected.FailureMeasurement.Unit)
	require.Equal(t, 123, projected.FailureMeasurement.ObservedAtLeast)
	require.Equal(t, "placement failure during assessment (timeout)", projected.Message)

	recorded, ok := projectSubmissionOperatorDiagnosticRecord(repository.SubmissionDiagnosticOperatorDiagnostic{
		ID:              strings.Repeat("i", 200),
		PlacementItemID: strings.Repeat("p", 200),
		OutcomeKind:     strings.Repeat("o", 120),
		Status:          strings.Repeat("s", 80),
		CreatedAt:       time.Date(2026, time.August, 18, 1, 0, 0, 0, time.FixedZone("test", 3600)),
		Payload:         payload,
	})
	require.True(t, ok)
	require.Len(t, recorded.ID, 128)
	require.Len(t, recorded.PlacementItemID, 128)
	require.Len(t, recorded.OutcomeKind, 96)
	require.Len(t, recorded.Status, 64)
	require.Equal(t, time.UTC, recorded.OccurredAt.Location())
}

func TestSubmissionDiagnosticProjectionRejectsUnsafeShapesAndCoversBounds(t *testing.T) {
	for _, payload := range []map[string]any{
		{},
		{"failure_reason_code": "provider-secret"},
		{"failure_measurement": map[string]any{"unit": "seconds", "limit": 1}},
		{"failure_measurement": map[string]any{"unit": "tokens", "limit": 0}},
	} {
		_, ok := projectSubmissionOperatorDiagnosticPayload(payload)
		require.False(t, ok)
	}
	require.Nil(t, projectSubmissionFailureMeasurement("not-an-object"))
	require.Nil(t, projectSubmissionFailureMeasurement(map[string]any{"unit": "tokens", "limit": float64(0)}))
	require.Equal(t, "placement failure: assessor_provider_failed", submissionOperatorDiagnosticMessage(SubmissionOperatorDiagnostic{FailureReasonCode: "assessor_provider_failed"}))
	require.Equal(t, "assessor response validation failed at response_json", submissionOperatorDiagnosticMessage(SubmissionOperatorDiagnostic{ValidationStage: "response_json"}))
	require.Equal(t, "placement failure requires operator review", submissionOperatorDiagnosticMessage(SubmissionOperatorDiagnostic{}))

	require.Equal(t, "", allowlistedDiagnosticToken("not-allowed", submissionDiagnosticStages, 64))
	validOther, ok := projectSubmissionOperatorDiagnosticPayload(map[string]any{
		"failure_class":             "malformed_response",
		"validation_field_families": []any{"other"},
	})
	require.True(t, ok)
	require.Equal(t, []string{"other"}, validOther.ValidationFieldFamilies)
	require.Equal(t, "truncated", boundedDiagnosticToken("truncated-too-long", 9))
	require.Equal(t, 0, boundedDiagnosticStatus(float64(99)))
	require.Equal(t, 599, boundedDiagnosticStatus(float64(600)))
	require.Equal(t, 7, boundedDiagnosticInt(int64(7), 100))
	require.Equal(t, 0, boundedDiagnosticInt(json.Number("bad"), 100))
	require.Equal(t, 0, boundedDiagnosticInt(-1, 100))
	require.Equal(t, 10, boundedDiagnosticInt(20, 10))
	valid, ok := projectSubmissionOperatorDiagnosticPayload(map[string]any{
		"failure_stage": "assessment",
		"failure_class": "timeout",
	})
	require.True(t, ok)
	require.Equal(t, "placement failure during assessment (timeout)", valid.Message)
	filtered := projectSubmissionOperatorDiagnostics([]repository.SubmissionDiagnosticOperatorDiagnostic{
		{Payload: map[string]any{"failure_stage": "unknown"}},
		{Payload: map[string]any{"failure_stage": "assessment"}},
	})
	require.Len(t, filtered, 1)
	require.Equal(t, "assessment", filtered[0].FailureStage)
}

func TestSubmissionDiagnosticsServiceHandlesUnavailableAndEmptyRepositories(t *testing.T) {
	var nilService *SubmissionDiagnosticsService
	_, err := nilService.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{})
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)
	_, err = nilService.GetSubmissionDiagnostic(context.Background(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)

	svc := NewSubmissionDiagnosticsService(nil)
	_, err = svc.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{})
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)

	repo := &submissionDiagnosticsRepoStub{err: errors.New("unavailable")}
	svc = NewSubmissionDiagnosticsService(repo)
	page, err := svc.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{})
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)
	require.Nil(t, page)

	repo.err = nil
	repo.page = nil
	page, err = svc.ListSubmissionDiagnostics(context.Background(), SubmissionDiagnosticFilter{Limit: 0, Offset: -1})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Equal(t, int64(0), page.Total)
	require.Equal(t, 20, repo.listFilter.Limit)
	require.Zero(t, repo.listFilter.Offset)

	_, err = svc.GetSubmissionDiagnostic(context.Background(), uuid.NewString(), "bad")
	require.ErrorContains(t, err, "submission_id")
	repo.detail = nil
	_, err = svc.GetSubmissionDiagnostic(context.Background(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrSubmissionDiagnosticsUnavailable)
}

func TestSubmissionDiagnosticSummaryNormalizesSourceText(t *testing.T) {
	trimmed := boundedSubmissionSourceSummary("  about\n\tthis   memory  ")
	require.Equal(t, "about this memory", trimmed.Value)
	require.False(t, trimmed.Truncated)
	signedURL := boundedSubmissionSourceSummary("https://memory.example.test/notes?X-Amz-Credential=operator&X-Amz-Signature=secret")
	require.Equal(t, "https://memory.example.test/notes", signedURL.Value)
	embeddedCredentialURL := boundedSubmissionSourceSummary("Imported from https://operator:supersecret@example.test/notes")
	require.Equal(t, "Imported from https://example.test/notes", embeddedCredentialURL.Value)
	credentialLabel := boundedSubmissionSourceSummary("source token=opaque-secret")
	require.Equal(t, "source token=[REDACTED]", credentialLabel.Value)
	credentialKey := boundedSubmissionSourceSummary("source credential=opaque-secret")
	require.Equal(t, "source credential=[REDACTED]", credentialKey.Value)
	compositeCredentialKey := boundedSubmissionSourceSummary("source AWS_SECRET_ACCESS_KEY=opaque-credential")
	require.Equal(t, "source AWS_SECRET_ACCESS_KEY=[REDACTED]", compositeCredentialKey.Value)
	quotedCompositeCredentialKey := boundedSubmissionSourceSummary(`source AWS_SECRET_ACCESS_KEY="opaque credential"`)
	require.Equal(t, "source AWS_SECRET_ACCESS_KEY=[REDACTED]", quotedCompositeCredentialKey.Value)
	passwordLabel := boundedSubmissionSourceSummary("source Password: supersecret")
	require.Equal(t, "source Password: [REDACTED]", passwordLabel.Value)
	accessTokenLabel := boundedSubmissionSourceSummary("source access_token: opaque-secret")
	require.Equal(t, "source access_token: [REDACTED]", accessTokenLabel.Value)
	cookieHeader := boundedSubmissionSourceSummary("request Cookie: session=opaque-secret")
	require.Equal(t, "request Cookie: session=[REDACTED]", cookieHeader.Value)
	cookieHeaderMultiple := boundedSubmissionSourceSummary("request Cookie: session=opaque; refresh=secret")
	require.Equal(t, "request Cookie: session=[REDACTED]", cookieHeaderMultiple.Value)
	cookieEquals := boundedSubmissionSourceSummary("browser Cookie=session=opaque-secret; refresh=secret")
	require.Equal(t, "browser Cookie=[REDACTED]", cookieEquals.Value)
	apiKeyHeader := boundedSubmissionSourceSummary("source X-API-Key: opaque-secret")
	require.Equal(t, "source X-API-Key: [REDACTED]", apiKeyHeader.Value)
	authorizationHeader := boundedSubmissionSourceSummary("source Authorization: Basic dXNlcjpwYXNz")
	require.Equal(t, "source Authorization: [REDACTED]", authorizationHeader.Value)
	authorizationToken := boundedSubmissionSourceSummary("source Authorization: opaque-secret")
	require.Equal(t, "source Authorization: [REDACTED]", authorizationToken.Value)
	digestAuthorization := boundedSubmissionSourceSummary(`source Authorization: Digest username="user", realm="private", response="secret"`)
	require.Equal(t, "source Authorization: [REDACTED]", digestAuthorization.Value)
	authorizationEquals := boundedSubmissionSourceSummary("source Authorization=Basic dXNlcjpwYXNz")
	require.Equal(t, "source Authorization=[REDACTED]", authorizationEquals.Value)
	require.Equal(t, boundedSubmissionText{}, boundedSubmissionSourceSummary(" \n\t "))
	long := boundedSubmissionSourceSummary(strings.Repeat("界", submissionSourceSummaryMaxRunes+1))
	require.Len(t, []rune(long.Value), submissionSourceSummaryMaxRunes)
	require.True(t, long.Truncated)
}
