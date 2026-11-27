package topics

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	assignment "github.com/markhuangai/dense-mem/internal/topics/assignment"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
)

type IncrementalAssignments interface {
	ReadSources(context.Context, string, []contract.SourceHandle) ([]contract.SourceSnapshot, error)
	CommitMaintenanceItemAssignment(context.Context, contract.MaintenanceClaim, assignment.Publication) (contract.OrganizationOutcome, error)
}

func (s *MaintenanceService) WithTopicProcessing(assigner *AssignmentService, store IncrementalAssignments, communities contract.CommunityRepository, placement CommunityAssessor, decisions modelprovider.DecisionSettings) *MaintenanceService {
	s.deps.Communities, s.deps.Placement = communities, placement
	s.deps.Equivalence, _ = store.(contract.EquivalenceMaintenanceRepository)
	s.deps.Consolidation, _ = store.(contract.TopicConsolidationRepository)
	s.deps.Decisions, s.deps.DecisionSettings = assigner.decisions, decisions
	s.deps.Assign = func(ctx context.Context, claim contract.MaintenanceClaim, policy domain.TopicMaintenanceConfig, accounting assessment.AttemptAccounting) (contract.OrganizationResult, error) {
		result := contract.OrganizationResult{IsolatedOutcomes: true}
		snapshots, err := store.ReadSources(ctx, claim.TeamID, claim.Sources)
		var itemErrors []error
		if errors.Is(err, contract.ErrSourceStale) {
			snapshots = nil
			for _, source := range claim.Sources {
				values, sourceErr := store.ReadSources(ctx, claim.TeamID, []contract.SourceHandle{source})
				if errors.Is(sourceErr, contract.ErrSourceStale) {
					result.Outcomes = append(result.Outcomes, contract.OrganizationOutcome{Source: source, Status: "pending", Reason: "stale_input"})
					itemErrors = append(itemErrors, sourceErr)
					continue
				}
				if sourceErr != nil {
					return result, sourceErr
				}
				snapshots = append(snapshots, values...)
			}
			err = nil
		}
		if err != nil {
			return result, err
		}
		if len(snapshots) == 0 {
			result.Current = true
			return result, errors.Join(itemErrors...)
		}
		slices := make([]assignment.Slice, len(snapshots))
		for i, snapshot := range snapshots {
			fingerprint, err := contract.SourceFingerprint(snapshot)
			if err != nil {
				return result, err
			}
			slices[i] = assignment.Slice{EvidenceID: snapshot.ID, SourceFingerprint: fingerprint, Index: i, Text: contract.SourceDisplayText(snapshot), PriorInputHash: claim.PriorAssignmentHashes[contract.SourceKey(snapshot.SourceHandle)]}
		}
		var settings *domain.DecisionRuntimeConfig
		if assigner.decisions != nil {
			if decisions == nil {
				return result, errors.New("decision settings unavailable for topic assignment")
			}
			value, err := decisions.DecisionRuntimeConfig(ctx)
			if err != nil {
				return result, err
			}
			settings = &value
		}
		input := assignment.Scope{TeamID: claim.TeamID, SpaceID: claim.SpaceID, Generation: claim.Generation, OperationID: claim.ID}
		outcomes, err := assigner.PrepareMaintenance(ctx, input, slices, settings, policy, accounting)
		if err != nil {
			return result, err
		}
		if len(outcomes) == 0 {
			return result, contract.ErrMaintenanceDisabled
		}
		for _, outcome := range outcomes {
			if outcome.Index < 0 || outcome.Index >= len(snapshots) {
				return result, errors.New("topic assignment returned an unknown Evidence index")
			}
			snapshot := snapshots[outcome.Index]
			itemErr := outcome.Failure
			var committed contract.OrganizationOutcome
			commitAttempted := itemErr == nil
			if itemErr == nil {
				committed, itemErr = store.CommitMaintenanceItemAssignment(ctx, claim, assignment.Publication{EvidenceID: snapshot.ID, Outcome: outcome, ExpectedFingerprint: slices[outcome.Index].SourceFingerprint})
			}
			if itemErr != nil {
				reason := contract.MaintenanceFailureCode(itemErr)
				if reason == "" {
					reason = "organization_failed"
				}
				status := "failed"
				if errors.Is(itemErr, contract.ErrSourceStale) || errors.Is(itemErr, assignment.ErrStale) {
					status, reason = "pending", "stale_input"
				}
				if commitAttempted && s.deps.Logger != nil {
					observed := "unavailable"
					var rejection *contract.PublicationRejection
					if errors.As(itemErr, &rejection) && rejection.Observed != "" {
						observed = rejection.Observed
					}
					s.deps.Logger.WarnContext(ctx, "topic_maintenance.item_commit_rejected", "team_id", claim.TeamID,
						"space_id", claim.SpaceID, "space_generation", claim.Generation, "run_id", claim.RunID,
						"batch_id", claim.ID, "stage", "assignment", "evidence_id", snapshot.ID,
						"expected_revision", claim.Revisions[contract.SourceKey(snapshot.SourceHandle)],
						"observed_revision", observed, "reason", reason, "error", itemErr)
				}
				committed = contract.OrganizationOutcome{Source: snapshot.SourceHandle, Status: status, Reason: reason}
				itemErrors = append(itemErrors, itemErr)
			}
			result.Outcomes = append(result.Outcomes, committed)
		}
		result.Current = true
		return result, errors.Join(itemErrors...)
	}
	return s
}
