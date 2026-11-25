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
	CommitMaintenanceAssignments(context.Context, contract.MaintenanceClaim, []assignment.Publication) ([]contract.OrganizationOutcome, error)
}

func (s *MaintenanceService) WithTopicProcessing(assigner *AssignmentService, store IncrementalAssignments, communities contract.CommunityRepository, placement CommunityAssessor, decisions modelprovider.DecisionSettings) *MaintenanceService {
	s.deps.Communities, s.deps.Placement = communities, placement
	s.deps.Assign = func(ctx context.Context, claim contract.MaintenanceClaim, policy domain.TopicMaintenanceConfig, accounting assessment.AttemptAccounting) (contract.OrganizationResult, error) {
		var result contract.OrganizationResult
		snapshots, err := store.ReadSources(ctx, claim.TeamID, claim.Sources)
		if err != nil {
			return result, err
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
		publications := make([]assignment.Publication, 0, len(outcomes))
		for _, outcome := range outcomes {
			if outcome.Index < 0 || outcome.Index >= len(snapshots) {
				return result, errors.New("topic assignment returned an unknown Evidence index")
			}
			snapshot := snapshots[outcome.Index]
			publications = append(publications, assignment.Publication{EvidenceID: snapshot.ID, Outcome: outcome, ExpectedFingerprint: slices[outcome.Index].SourceFingerprint})
		}
		result.Outcomes, err = store.CommitMaintenanceAssignments(ctx, claim, publications)
		result.Current = err == nil
		return result, err
	}
	return s
}
