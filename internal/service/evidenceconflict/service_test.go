package evidenceconflict

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
)

type evidenceConflictStoreStub struct {
	listInput     repository.EvidenceConflictListInput
	getInput      repository.EvidenceConflictGetInput
	resolveInput  repository.EvidenceConflictResolutionInput
	listResult    *repository.EvidenceConflictListResult
	getResult     *repository.EvidenceConflictGetResult
	listErr       error
	getErr        error
	resolveErr    error
	resolveResult *repository.EvidenceConflictCaseRecord
}

func (s *evidenceConflictStoreStub) ListEvidenceConflicts(_ context.Context, input repository.EvidenceConflictListInput) (*repository.EvidenceConflictListResult, error) {
	s.listInput = input
	return s.listResult, s.listErr
}

func (s *evidenceConflictStoreStub) GetEvidenceConflict(_ context.Context, input repository.EvidenceConflictGetInput) (*repository.EvidenceConflictGetResult, error) {
	s.getInput = input
	return s.getResult, s.getErr
}

func (s *evidenceConflictStoreStub) ResolveEvidenceConflict(_ context.Context, input repository.EvidenceConflictResolutionInput) (*repository.EvidenceConflictCaseRecord, error) {
	s.resolveInput = input
	return s.resolveResult, s.resolveErr
}

func TestListDefaultsOpenAndEncodesTeamStatusCursor(t *testing.T) {
	teamID := uuid.NewString()
	conflictID := uuid.NewString()
	updatedAt := time.Now().UTC().Truncate(time.Microsecond)
	store := &evidenceConflictStoreStub{listResult: &repository.EvidenceConflictListResult{
		Items:      []repository.EvidenceConflictCaseRecord{},
		NextCursor: &repository.EvidenceConflictCursor{Version: 1, TeamID: teamID, StatusFilter: "open", UpdatedAt: updatedAt, ConflictID: conflictID},
	}}
	page, err := New(store).List(context.Background(), teamID, ListOptions{})
	require.NoError(t, err)
	require.Equal(t, "open", store.listInput.Status)
	require.Equal(t, repository.EvidenceConflictDefaultLimit, store.listInput.Limit)
	require.NotNil(t, page.NextCursor)
	decoded, err := repository.DecodeEvidenceConflictCursor(*page.NextCursor)
	require.NoError(t, err)
	require.Equal(t, teamID, decoded.TeamID)
	require.Equal(t, "open", decoded.StatusFilter)
}

func TestListRejectsCursorFromAnotherTeam(t *testing.T) {
	teamID := uuid.NewString()
	cursor, err := repository.EncodeEvidenceConflictCursor(repository.EvidenceConflictCursor{
		Version: 1, TeamID: uuid.NewString(), StatusFilter: "open", UpdatedAt: time.Now().UTC(), ConflictID: uuid.NewString(),
	})
	require.NoError(t, err)
	store := &evidenceConflictStoreStub{listResult: &repository.EvidenceConflictListResult{Items: []repository.EvidenceConflictCaseRecord{}}}
	_, err = New(store).List(context.Background(), teamID, ListOptions{Cursor: cursor})
	require.ErrorIs(t, err, ErrInvalidCursor)
	require.Empty(t, store.listInput.TeamID)
}

func TestGetMapsRepositoryNotFoundToBoundedNotFound(t *testing.T) {
	store := &evidenceConflictStoreStub{getErr: repository.ErrEvidenceConflictNotFound}
	_, err := New(store).Get(context.Background(), uuid.NewString(), uuid.NewString(), 0, "")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestResolvePreservesVersionFenceAndDecision(t *testing.T) {
	teamID, conflictID, preferredID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := &evidenceConflictStoreStub{resolveErr: repository.ErrEvidenceConflictVersionStale}
	_, err := New(store).Resolve(context.Background(), ResolutionInput{
		TeamID: teamID, ConflictID: conflictID, ExpectedVersion: 3, Decision: "resolve", Reason: "reviewed", PreferredPositionID: preferredID,
	})
	require.ErrorIs(t, err, ErrVersionStale)
	require.Equal(t, teamID, store.resolveInput.TeamID)
	require.Equal(t, conflictID, store.resolveInput.ConflictID)
	require.Equal(t, 3, store.resolveInput.ExpectedVersion)
	require.Equal(t, "resolve", store.resolveInput.Decision)
	require.Equal(t, preferredID, store.resolveInput.PreferredPositionID)
}

func TestListValidatesInputsAndMapsStoreFailures(t *testing.T) {
	teamID := uuid.NewString()
	cases := []struct {
		name  string
		input ListOptions
		want  error
	}{
		{name: "invalid status", input: ListOptions{Status: "other"}, want: ErrInvalidStatus},
		{name: "invalid limit", input: ListOptions{Limit: repository.EvidenceConflictMaxLimit + 1}, want: ErrInvalidLimit},
		{name: "invalid team", want: ErrInvalid},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			id := teamID
			if testCase.name == "invalid team" {
				id = "not-a-uuid"
			}
			_, err := New(&evidenceConflictStoreStub{}).List(context.Background(), id, testCase.input)
			require.ErrorIs(t, err, testCase.want)
		})
	}
	store := &evidenceConflictStoreStub{listErr: context.DeadlineExceeded}
	_, err := New(store).List(context.Background(), teamID, ListOptions{Status: "resolved", Limit: 1})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	store.listErr = nil
	store.listResult = nil
	_, err = New(store).List(context.Background(), teamID, ListOptions{})
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestListReturnsEmptyItemsAndRejectsMismatchedStatusCursor(t *testing.T) {
	teamID, conflictID := uuid.NewString(), uuid.NewString()
	cursor, err := repository.EncodeEvidenceConflictCursor(repository.EvidenceConflictCursor{
		Version: 1, TeamID: teamID, StatusFilter: "resolved", UpdatedAt: time.Now().UTC(), ConflictID: conflictID,
	})
	require.NoError(t, err)
	store := &evidenceConflictStoreStub{listResult: &repository.EvidenceConflictListResult{}}
	page, err := New(store).List(context.Background(), teamID, ListOptions{Status: "resolved", Cursor: cursor})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Nil(t, page.NextCursor)
	_, err = New(store).List(context.Background(), teamID, ListOptions{Status: "open", Cursor: cursor})
	require.ErrorIs(t, err, ErrInvalidCursor)
}

func TestGetValidatesInputsAndReturnsDetail(t *testing.T) {
	teamID, conflictID := uuid.NewString(), uuid.NewString()
	store := &evidenceConflictStoreStub{getResult: &repository.EvidenceConflictGetResult{
		Conflict:        &repository.EvidenceConflictCaseRecord{TeamID: teamID, ConflictID: conflictID},
		NextEventCursor: &repository.EvidenceConflictEventCursor{Version: 1, TeamID: teamID, ConflictID: conflictID, Ordinal: 1, EventID: uuid.NewString()},
	}}
	detail, err := New(store).Get(context.Background(), teamID, conflictID, 1, "")
	require.NoError(t, err)
	require.Equal(t, conflictID, detail.Conflict.ConflictID)
	require.NotEmpty(t, detail.NextEventCursor)
	cursor, err := repository.EncodeEvidenceConflictEventCursor(repository.EvidenceConflictEventCursor{
		Version: 1, TeamID: teamID, ConflictID: conflictID, Ordinal: 1, EventID: uuid.NewString(),
	})
	require.NoError(t, err)
	_, err = New(store).Get(context.Background(), teamID, conflictID, 1, cursor)
	require.NoError(t, err)
	require.NotNil(t, store.getInput.EventCursor)
	for _, testCase := range []struct {
		name  string
		team  string
		id    string
		limit int
		want  error
	}{
		{name: "invalid team", team: "bad", id: conflictID, want: ErrInvalid},
		{name: "invalid conflict", team: teamID, id: "bad", want: ErrInvalid},
		{name: "invalid limit", team: teamID, id: conflictID, limit: repository.EvidenceConflictMaxEventLimit + 1, want: ErrInvalidLimit},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := New(store).Get(context.Background(), testCase.team, testCase.id, testCase.limit, "")
			require.ErrorIs(t, err, testCase.want)
		})
	}
	store.getResult = &repository.EvidenceConflictGetResult{}
	_, err = New(store).Get(context.Background(), teamID, conflictID, 1, "")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestResolveMapsValidationAndRepositoryErrors(t *testing.T) {
	teamID, conflictID, preferredID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	service := New(&evidenceConflictStoreStub{})
	invalid := []ResolutionInput{
		{TeamID: "bad", ConflictID: conflictID, ExpectedVersion: 1, Decision: "resolve", Reason: "ok"},
		{TeamID: teamID, ConflictID: "bad", ExpectedVersion: 1, Decision: "resolve", Reason: "ok"},
		{TeamID: teamID, ConflictID: conflictID, ExpectedVersion: 0, Decision: "resolve", Reason: "ok"},
		{TeamID: teamID, ConflictID: conflictID, ExpectedVersion: 1, Decision: "other", Reason: "ok"},
		{TeamID: teamID, ConflictID: conflictID, ExpectedVersion: 1, Decision: "dismiss", Reason: "ok", PreferredPositionID: preferredID},
		{TeamID: teamID, ConflictID: conflictID, ExpectedVersion: 1, Decision: "resolve", Reason: "ok", PreferredPositionID: "bad"},
	}
	for index, input := range invalid {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			_, err := service.Resolve(context.Background(), input)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
	store := service.store.(*evidenceConflictStoreStub)
	for _, testCase := range []struct {
		name string
		err  error
		want error
	}{
		{name: "not found", err: repository.ErrEvidenceConflictNotFound, want: ErrNotFound},
		{name: "stale", err: repository.ErrEvidenceConflictVersionStale, want: ErrVersionStale},
		{name: "not open", err: repository.ErrEvidenceConflictNotOpen, want: ErrNotOpen},
		{name: "invalid", err: repository.ErrEvidenceConflictInvalidCommand, want: ErrInvalid},
		{name: "other", err: context.Canceled, want: context.Canceled},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store.resolveErr = testCase.err
			_, err := service.Resolve(context.Background(), ResolutionInput{TeamID: teamID, ConflictID: conflictID, ExpectedVersion: 1, Decision: "resolve", Reason: "reviewed"})
			require.ErrorIs(t, err, testCase.want)
		})
	}
	store.resolveErr = nil
	store.resolveResult = &repository.EvidenceConflictCaseRecord{ConflictID: conflictID}
	result, err := service.Resolve(context.Background(), ResolutionInput{TeamID: teamID, ConflictID: conflictID, ExpectedVersion: 1, Decision: "resolve", Reason: "reviewed"})
	require.NoError(t, err)
	require.Equal(t, conflictID, result.ConflictID)
}

func TestEvidenceConflictServiceHandlesUnavailableAndCursorEncodingFailures(t *testing.T) {
	teamID, conflictID := uuid.NewString(), uuid.NewString()
	var nilService *Service
	_, err := nilService.List(context.Background(), teamID, ListOptions{})
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = nilService.Get(context.Background(), teamID, conflictID, 1, "")
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = nilService.Resolve(context.Background(), ResolutionInput{TeamID: teamID, ConflictID: conflictID, ExpectedVersion: 1, Decision: "resolve", Reason: "ok"})
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = New(nil).List(context.Background(), teamID, ListOptions{})
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = New(nil).Get(context.Background(), teamID, conflictID, 1, "")
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = New(nil).Resolve(context.Background(), ResolutionInput{TeamID: teamID, ConflictID: conflictID, ExpectedVersion: 1, Decision: "resolve", Reason: "ok"})
	require.ErrorIs(t, err, ErrUnavailable)

	badCursorStore := &evidenceConflictStoreStub{listResult: &repository.EvidenceConflictListResult{
		NextCursor: &repository.EvidenceConflictCursor{Version: 2, TeamID: teamID, StatusFilter: "open", UpdatedAt: time.Now().UTC(), ConflictID: conflictID},
	}}
	_, err = New(badCursorStore).List(context.Background(), teamID, ListOptions{})
	require.ErrorIs(t, err, ErrUnavailable)
	badEventCursorStore := &evidenceConflictStoreStub{getResult: &repository.EvidenceConflictGetResult{
		Conflict:        &repository.EvidenceConflictCaseRecord{TeamID: teamID, ConflictID: conflictID},
		NextEventCursor: &repository.EvidenceConflictEventCursor{Version: 2, TeamID: teamID, ConflictID: conflictID, Ordinal: 1, EventID: uuid.NewString()},
	}}
	_, err = New(badEventCursorStore).Get(context.Background(), teamID, conflictID, 1, "")
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestEvidenceConflictServiceRejectsMalformedEventCursorAndMapsGetFailure(t *testing.T) {
	teamID, conflictID := uuid.NewString(), uuid.NewString()
	store := &evidenceConflictStoreStub{getErr: context.Canceled}
	_, err := New(store).Get(context.Background(), teamID, conflictID, 1, "not-a-cursor")
	require.ErrorIs(t, err, ErrInvalidCursor)
	_, err = New(store).Get(context.Background(), teamID, conflictID, 1, " ")
	// A non-empty valid request reaches the repository and preserves its error.
	require.ErrorIs(t, err, context.Canceled)
}
