package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

type invocationDiagnosticsRepoStub struct {
	page    *knowledgecontract.RememberInvocationDiagnosticRecordPage
	record  *knowledgecontract.RememberInvocationDiagnosticRecord
	filter  knowledgecontract.RememberInvocationDiagnosticFilter
	listErr error
	getErr  error
}

func (s *invocationDiagnosticsRepoStub) RecordRememberInvocationDiagnostic(context.Context, knowledgecontract.RememberInvocationDiagnosticInput) error {
	return nil
}
func (s *invocationDiagnosticsRepoStub) ListRememberInvocationDiagnostics(_ context.Context, filter knowledgecontract.RememberInvocationDiagnosticFilter) (*knowledgecontract.RememberInvocationDiagnosticRecordPage, error) {
	s.filter = filter
	return s.page, s.listErr
}
func (s *invocationDiagnosticsRepoStub) GetRememberInvocationDiagnostic(context.Context, string, string) (*knowledgecontract.RememberInvocationDiagnosticRecord, error) {
	if s.record == nil && s.page != nil && len(s.page.Records) > 0 {
		return &s.page.Records[0], s.getErr
	}
	return s.record, s.getErr
}

func TestRememberInvocationDiagnosticsProjectsBoundedListAndDetail(t *testing.T) {
	createdAt := time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(24 * time.Hour)
	repo := &invocationDiagnosticsRepoStub{page: &knowledgecontract.RememberInvocationDiagnosticRecordPage{
		Total: 1,
		Records: []knowledgecontract.RememberInvocationDiagnosticRecord{{
			TeamID: "00000000-0000-4000-8000-000000000001", OwnerProfileID: "00000000-0000-4000-8000-000000000002",
			InvocationID: "00000000-0000-4000-8000-000000000003", RequestHash: "hash", Classification: "replay", Outcome: "replayed",
			Retryable: true, CreatedAt: createdAt, ExpiresAt: expiresAt,
			RequestBody: []byte(`{"secret":"admitted"}`), ProviderExchanges: []knowledgecontract.RememberAttemptDiagnosticInput{{SequenceNo: 1, Kind: "provider_exchange", Component: "assessor", ResponseBody: []byte(`{"status":200}`), ExpiresAt: createdAt.Add(7 * 24 * time.Hour)}},
		}},
	}}
	service := NewRememberInvocationDiagnosticsService(repo)
	page, err := service.ListRememberInvocationDiagnostics(context.Background(), RememberInvocationDiagnosticFilter{
		TeamID: repo.page.Records[0].TeamID, Classification: "replay", Retryable: boolPtr(true), Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "hash", page.Items[0].RequestHash)
	require.NotNil(t, repo.filter.Retryable)
	require.True(t, *repo.filter.Retryable)

	detail, err := service.GetRememberInvocationDiagnostic(context.Background(), repo.page.Records[0].TeamID, repo.page.Records[0].InvocationID)
	require.NoError(t, err)
	require.Equal(t, "replay", detail.Classification)
	require.Equal(t, "unknown_receipt", detail.DeliveryStage)
	require.Contains(t, detail.RequestBody, "admitted")
	require.Len(t, detail.ProviderExchanges, 1)
	require.Equal(t, createdAt, detail.ProviderExchanges[0].CapturedAt)
	require.Equal(t, expiresAt, detail.ProviderExchanges[0].ExpiresAt)
}

func boolPtr(value bool) *bool { return &value }

func TestRememberInvocationDiagnosticsValidationAndFailures(t *testing.T) {
	var nilService *RememberInvocationDiagnosticsService
	_, err := nilService.ListRememberInvocationDiagnostics(context.Background(), RememberInvocationDiagnosticFilter{})
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticsUnavailable)
	_, err = nilService.GetRememberInvocationDiagnostic(context.Background(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticsUnavailable)

	service := NewRememberInvocationDiagnosticsService(nil)
	_, err = service.ListRememberInvocationDiagnostics(context.Background(), RememberInvocationDiagnosticFilter{})
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticsUnavailable)
	_, err = service.GetRememberInvocationDiagnostic(context.Background(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticsUnavailable)

	repo := &invocationDiagnosticsRepoStub{}
	service = NewRememberInvocationDiagnosticsService(repo)
	for _, filter := range []RememberInvocationDiagnosticFilter{
		{TeamID: "bad"}, {OwnerProfileID: "bad"}, {InvocationID: "bad"}, {CanonicalAttemptID: "bad"},
		{Classification: "unknown"}, {Outcome: "unknown"},
	} {
		_, err = service.ListRememberInvocationDiagnostics(context.Background(), filter)
		require.Error(t, err)
	}
	page, err := service.ListRememberInvocationDiagnostics(context.Background(), RememberInvocationDiagnosticFilter{Limit: -1, Offset: -1})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Equal(t, 50, repo.filter.Limit)
	require.Zero(t, repo.filter.Offset)

	repo.listErr = errors.New("database unavailable")
	_, err = service.ListRememberInvocationDiagnostics(context.Background(), RememberInvocationDiagnosticFilter{})
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticsUnavailable)
	repo.listErr = nil
	repo.page = nil
	page, err = service.ListRememberInvocationDiagnostics(context.Background(), RememberInvocationDiagnosticFilter{})
	require.NoError(t, err)
	require.Empty(t, page.Items)

	_, err = service.GetRememberInvocationDiagnostic(context.Background(), "bad", uuid.NewString())
	require.Error(t, err)
	_, err = service.GetRememberInvocationDiagnostic(context.Background(), uuid.NewString(), "bad")
	require.Error(t, err)
	repo.getErr = knowledgecontract.ErrRememberInvocationDiagnosticNotFound
	_, err = service.GetRememberInvocationDiagnostic(context.Background(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticNotFound)
	repo.getErr = errors.New("database unavailable")
	_, err = service.GetRememberInvocationDiagnostic(context.Background(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticsUnavailable)
	repo.getErr = nil
	repo.record = nil
	_, err = service.GetRememberInvocationDiagnostic(context.Background(), uuid.NewString(), uuid.NewString())
	require.ErrorIs(t, err, ErrRememberInvocationDiagnosticsUnavailable)
}
