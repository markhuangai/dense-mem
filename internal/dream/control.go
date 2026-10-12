package dream

import (
	"context"
	"errors"
	"fmt"
	httperr "github.com/markhuangai/dense-mem/internal/httperr"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

type ControlDependencies struct {
	Store dreamcontract.DreamControlRepository
}

type ControlService interface {
	List(ctx context.Context, teamID string, opts ListOptions) ([]*domain.Dream, string, error)
	Get(ctx context.Context, teamID, dreamID string) (*domain.Dream, error)
	ListRuns(ctx context.Context, teamID string, limit int) ([]*RunCycleResult, error)
	Status(ctx context.Context, teamID string) (*StatusResult, error)
}

type controlService struct {
	deps ControlDependencies
}

var _ ControlService = (*controlService)(nil)

func NewControl(deps ControlDependencies) ControlService {
	return &controlService{deps: deps}
}

func (s *controlService) List(ctx context.Context, teamID string, opts ListOptions) ([]*domain.Dream, string, error) {
	if s.deps.Store == nil {
		return nil, "", fmt.Errorf("control dream list: dream repository is required")
	}
	records, next, err := s.deps.Store.ListHypotheses(ctx, dreamcontract.ListHypothesesInput{
		TeamID:    teamID,
		Status:    opts.Status,
		Limit:     opts.Limit,
		Cursor:    opts.Cursor,
		Sort:      opts.Sort,
		Direction: opts.Direction,
	})
	if err != nil {
		return nil, "", translateDreamRepositoryError(err)
	}
	return dreamRecords(records), next, nil
}

func (s *controlService) Get(ctx context.Context, teamID, dreamID string) (*domain.Dream, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("control dream get: dream repository is required")
	}
	record, err := s.deps.Store.GetHypothesis(ctx, dreamcontract.GetHypothesisInput{
		TeamID:       teamID,
		HypothesisID: dreamID,
	})
	if err != nil {
		if errors.Is(err, dreamcontract.ErrDreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, translateDreamRepositoryError(err)
	}
	return dreamRecord(record), nil
}

func (s *controlService) ListRuns(ctx context.Context, teamID string, limit int) ([]*RunCycleResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("control dream cycle runs: dream repository is required")
	}
	runs, err := s.deps.Store.ListDreamCyclesForTeam(ctx, teamID, limit)
	if err != nil {
		return nil, translateDreamRepositoryError(err)
	}
	results := make([]*RunCycleResult, 0, len(runs))
	for i := range runs {
		results = append(results, cycleRunResult(&runs[i]))
	}
	return results, nil
}

func (s *controlService) Status(ctx context.Context, teamID string) (*StatusResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("control dream status: dream repository is required")
	}
	pending, err := s.deps.Store.CountHypotheses(ctx, teamID, string(domain.DreamStatusProposed))
	if err != nil {
		return nil, translateDreamRepositoryError(err)
	}
	runs, err := s.deps.Store.ListDreamCyclesForTeam(ctx, teamID, 1)
	if err != nil {
		return nil, translateDreamRepositoryError(err)
	}
	var latest *RunCycleResult
	if len(runs) > 0 {
		latest = cycleRunResult(&runs[0])
	}
	return &StatusResult{Retired: true, LatestRun: latest, HistoricalProposedCount: pending}, nil
}

func translateDreamRepositoryError(err error) error {
	if errors.Is(err, dreamcontract.ErrTeamInactive) {
		return httperr.New(httperr.NOT_FOUND, "team not found")
	}
	return err
}
func dreamRecords(records []dreamcontract.HypothesisRecord) []*domain.Dream {
	out := make([]*domain.Dream, 0, len(records))
	for i := range records {
		out = append(out, dreamRecord(&records[i]))
	}
	return out
}
func dreamRecord(record *dreamcontract.HypothesisRecord) *domain.Dream {
	if record == nil {
		return nil
	}
	return &domain.Dream{
		DreamID:                        record.HypothesisID,
		TeamID:                         record.TeamID,
		Hypothesis:                     record.Statement,
		WhatIf:                         anyString(record.Payload["what_if"]),
		PossibleOutcome:                anyString(record.Payload["possible_outcome"]),
		Rationale:                      record.Rationale,
		Likelihood:                     floatPtrValue(record.Likelihood),
		Confidence:                     floatPtrValue(record.Confidence),
		SourceOwnerProfileIDs:          append([]string(nil), record.SourceOwnerProfileIDs...),
		SubjectEntityID:                record.SubjectEntityID,
		PredicateKey:                   record.PredicateKey,
		ObjectEntityID:                 record.ObjectEntityID,
		ObjectValueID:                  record.ObjectValueID,
		SourceRelationshipIDs:          dreamSourceIDs(record.SourceRefs, false),
		SourceCandidateRelationshipIDs: dreamSourceIDs(record.SourceRefs, true),
		SourceVersions:                 copyDreamSourceVersions(record.SourceVersions),
		GeneratorKind:                  firstNonEmpty(record.GeneratorKind, "deterministic"),
		GeneratorVersion:               firstNonEmpty(record.GeneratorVersion, "dream-v2"),
		Status:                         domain.DreamStatus(record.Status),
		Lane:                           record.Lane,
		CycleRunID:                     record.CycleRunID,
		GeneratorModel:                 firstNonEmpty(record.GeneratorVersion, record.GeneratorKind),
		ContentHash:                    record.ContentHash,
		SourceRefs:                     dreamSourceRefs(record.SourceRefs),
		Derivations:                    dreamDerivations(record.Derivations),
		SourceEvidenceIDs:              append([]string(nil), record.SourceEvidenceIDs...),
		EvidenceDerivations:            dreamEvidenceDerivations(record.EvidenceDerivations),
		InvalidatedReason:              record.InvalidatedReason,
		CreatedAt:                      record.CreatedAt,
		UpdatedAt:                      record.UpdatedAt,
	}
}
func dreamDerivations(derivations []dreamcontract.DreamDerivationSource) []domain.DreamDerivation {
	out := make([]domain.DreamDerivation, 0, len(derivations))
	for _, derivation := range derivations {
		out = append(out, domain.DreamDerivation{
			PremisePosition:     derivation.PremisePosition,
			RelationshipID:      derivation.RelationshipID,
			RelationshipVersion: derivation.RelationshipVersion,
			SourceGroupKey:      derivation.SourceGroupKey,
			Quote:               derivation.Quote,
			Authority:           derivation.Authority,
		})
	}
	return out
}
func dreamEvidenceDerivations(derivations []dreamcontract.EvidenceDerivationSource) []domain.DreamEvidenceDerivation {
	out := make([]domain.DreamEvidenceDerivation, 0, len(derivations))
	for _, derivation := range derivations {
		out = append(out, domain.DreamEvidenceDerivation{
			EvidenceID:     derivation.EvidenceID,
			SourceGroupKey: derivation.SourceGroupKey,
			SpanStart:      derivation.SpanStart,
			SpanEnd:        derivation.SpanEnd,
			Quote:          derivation.Quote,
			Authority:      derivation.Authority,
		})
	}
	return out
}
func dreamSourceIDs(refs []map[string]any, candidates bool) []string {
	out := []string{}
	for _, ref := range refs {
		refType := strings.TrimSpace(anyString(ref["type"]))
		if candidates {
			if refType != "candidate_relationship" {
				continue
			}
		} else if refType != "relationship" {
			continue
		}
		id := strings.TrimSpace(anyString(ref["id"]))
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}
func copyDreamSourceVersions(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
func cycleRunResult(run *dreamcontract.DreamCycleRun) *RunCycleResult {
	if run == nil {
		return nil
	}
	completed := time.Time{}
	if run.CompletedAt != nil {
		completed = *run.CompletedAt
	}
	scheduledFor := time.Time{}
	if run.ScheduledFor != nil {
		scheduledFor = *run.ScheduledFor
	}
	return &RunCycleResult{
		RunID:                    run.RunID,
		TeamID:                   run.TeamID,
		RunDate:                  run.RunDate,
		StartedAt:                run.StartedAt,
		CompletedAt:              completed,
		InputRelationships:       run.InputCount,
		CreatedDreams:            run.CreatedHypotheses,
		RejectedDreams:           run.RejectedHypotheses,
		ScheduledFor:             scheduledFor,
		AttemptCount:             run.AttemptCount,
		ProviderModel:            run.ProviderModel,
		ProviderTurns:            run.ProviderTurns,
		ProviderInputTokens:      run.ProviderInputTokens,
		ProviderOutputTokens:     run.ProviderOutputTokens,
		AttemptedPaths:           run.AttemptedPaths,
		ProviderProposals:        run.ProviderProposals,
		OutcomeSummary:           copyDreamOutcomeSummary(run.OutcomeSummary),
		Status:                   run.Status,
		Error:                    run.Error,
		Lane:                     run.Lane,
		EvidenceTargets:          run.EvidenceTargets,
		EvaluatedEvidenceTargets: run.EvaluatedEvidenceTargets,
	}
}
func copyDreamOutcomeSummary(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
func dreamSourceRefs(values []map[string]any) []domain.DreamSourceRef {
	out := make([]domain.DreamSourceRef, 0, len(values))
	for _, value := range values {
		ref := domain.DreamSourceRef{
			Type: strings.TrimSpace(anyString(value["type"])),
			ID:   strings.TrimSpace(anyString(value["id"])),
		}
		if ref.Type != "" && ref.ID != "" {
			out = append(out, ref)
		}
	}
	return out
}
func floatPtrValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func anyString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}
