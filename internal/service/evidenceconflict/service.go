package evidenceconflict

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/repository"
)

var (
	ErrInvalidStatus = errors.New("invalid evidence conflict status")
	ErrInvalidLimit  = errors.New("invalid evidence conflict limit")
	ErrInvalidCursor = errors.New("invalid evidence conflict cursor")
	ErrNotFound      = errors.New("evidence conflict not found")
	ErrVersionStale  = errors.New("evidence conflict version is stale")
	ErrNotOpen       = errors.New("evidence conflict is not open")
	ErrInvalid       = errors.New("evidence conflict command is invalid")
	ErrUnavailable   = errors.New("evidence conflict service unavailable")
)

type ListOptions struct {
	Status string
	Limit  int
	Cursor string
}

type ListPage struct {
	Items      []repository.EvidenceConflictCaseRecord `json:"items"`
	NextCursor *string                                 `json:"next_cursor"`
}

type Detail struct {
	Conflict        *repository.EvidenceConflictCaseRecord `json:"conflict"`
	NextEventCursor *string                                `json:"next_event_cursor"`
}

type ResolutionInput struct {
	TeamID              string
	ConflictID          string
	ExpectedVersion     int
	Decision            string
	Reason              string
	PreferredPositionID string
	ActorKind           string
	ActorID             string
}

type Reader interface {
	List(context.Context, string, ListOptions) (*ListPage, error)
	Get(context.Context, string, string, int, string) (*Detail, error)
	Resolve(context.Context, ResolutionInput) (*repository.EvidenceConflictCaseRecord, error)
}

type Service struct {
	store repository.EvidenceConflictRepository
}

var _ Reader = (*Service)(nil)

func New(store repository.EvidenceConflictRepository) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context, teamID string, options ListOptions) (*ListPage, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, ErrInvalid
	}
	status := strings.ToLower(strings.TrimSpace(options.Status))
	if status == "" {
		status = "open"
	}
	if status != "open" && status != "resolved" && status != "dismissed" {
		return nil, ErrInvalidStatus
	}
	limit := options.Limit
	if limit == 0 {
		limit = repository.EvidenceConflictDefaultLimit
	}
	if limit < 1 || limit > repository.EvidenceConflictMaxLimit {
		return nil, ErrInvalidLimit
	}
	var cursor *repository.EvidenceConflictCursor
	if strings.TrimSpace(options.Cursor) != "" {
		decoded, err := repository.DecodeEvidenceConflictCursor(options.Cursor)
		if err != nil || decoded.TeamID != teamID || decoded.StatusFilter != status {
			return nil, ErrInvalidCursor
		}
		cursor = decoded
	}
	page, err := s.store.ListEvidenceConflicts(ctx, repository.EvidenceConflictListInput{TeamID: teamID, Status: status, Limit: limit, Cursor: cursor})
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, ErrUnavailable
	}
	result := &ListPage{Items: page.Items, NextCursor: nil}
	if page.NextCursor != nil {
		encoded, err := repository.EncodeEvidenceConflictCursor(*page.NextCursor)
		if err != nil {
			return nil, ErrUnavailable
		}
		result.NextCursor = &encoded
	}
	if result.Items == nil {
		result.Items = []repository.EvidenceConflictCaseRecord{}
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, teamID, conflictID string, eventLimit int, eventCursor string) (*Detail, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	teamID, conflictID = strings.TrimSpace(teamID), strings.TrimSpace(conflictID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, ErrInvalid
	}
	if _, err := uuid.Parse(conflictID); err != nil {
		return nil, ErrInvalid
	}
	if eventLimit == 0 {
		eventLimit = repository.EvidenceConflictDefaultEventLimit
	}
	if eventLimit < 1 || eventLimit > repository.EvidenceConflictMaxEventLimit {
		return nil, ErrInvalidLimit
	}
	var cursor *repository.EvidenceConflictEventCursor
	if strings.TrimSpace(eventCursor) != "" {
		decoded, err := repository.DecodeEvidenceConflictEventCursor(eventCursor)
		if err != nil || decoded.TeamID != teamID || decoded.ConflictID != conflictID {
			return nil, ErrInvalidCursor
		}
		cursor = decoded
	}
	detail, err := s.store.GetEvidenceConflict(ctx, repository.EvidenceConflictGetInput{TeamID: teamID, ConflictID: conflictID, EventLimit: eventLimit, EventCursor: cursor})
	if errors.Is(err, repository.ErrEvidenceConflictNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if detail == nil || detail.Conflict == nil {
		return nil, ErrNotFound
	}
	result := &Detail{Conflict: detail.Conflict}
	if detail.NextEventCursor != nil {
		encoded, err := repository.EncodeEvidenceConflictEventCursor(*detail.NextEventCursor)
		if err != nil {
			return nil, ErrUnavailable
		}
		result.NextEventCursor = &encoded
	}
	return result, nil
}

func (s *Service) Resolve(ctx context.Context, input ResolutionInput) (*repository.EvidenceConflictCaseRecord, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	input.TeamID, input.ConflictID = strings.TrimSpace(input.TeamID), strings.TrimSpace(input.ConflictID)
	input.Decision, input.Reason = strings.ToLower(strings.TrimSpace(input.Decision)), strings.TrimSpace(input.Reason)
	input.PreferredPositionID = strings.TrimSpace(input.PreferredPositionID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, ErrInvalid
	}
	if _, err := uuid.Parse(input.ConflictID); err != nil {
		return nil, ErrInvalid
	}
	if input.ExpectedVersion < 1 || input.Reason == "" || len([]rune(input.Reason)) > 512 || (input.Decision != "resolve" && input.Decision != "dismiss") {
		return nil, ErrInvalid
	}
	if input.Decision == "dismiss" && input.PreferredPositionID != "" {
		return nil, ErrInvalid
	}
	if input.PreferredPositionID != "" {
		if _, err := uuid.Parse(input.PreferredPositionID); err != nil {
			return nil, ErrInvalid
		}
	}
	if input.ActorKind == "" {
		input.ActorKind = "control"
	}
	record, err := s.store.ResolveEvidenceConflict(ctx, repository.EvidenceConflictResolutionInput{
		TeamID: input.TeamID, ConflictID: input.ConflictID, ExpectedVersion: input.ExpectedVersion,
		Decision: input.Decision, Reason: input.Reason, PreferredPositionID: input.PreferredPositionID,
		ActorKind: input.ActorKind, ActorID: input.ActorID,
	})
	if errors.Is(err, repository.ErrEvidenceConflictNotFound) {
		return nil, ErrNotFound
	}
	if errors.Is(err, repository.ErrEvidenceConflictVersionStale) {
		return nil, ErrVersionStale
	}
	if errors.Is(err, repository.ErrEvidenceConflictNotOpen) {
		return nil, ErrNotOpen
	}
	if errors.Is(err, repository.ErrEvidenceConflictInvalidCommand) {
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("resolve evidence conflict: %w", err)
	}
	return record, nil
}
