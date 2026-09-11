package recall

import (
	"errors"

	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

var ErrRecallRepositoryTeamMismatch = errors.New("recall: repository returned a mismatched team")

type RecallConflictPosition = recallcontract.RecallConflictPosition
type RecallConflictSupporter = recallcontract.RecallConflictSupporter

func validateRecallConflictTeams(recalled *recallcontract.RecallEvidenceResult, expectedTeamID string) error {
	if recalled == nil {
		return nil
	}
	for _, conflict := range recalled.Conflicts {
		if conflict.TeamID != expectedTeamID {
			return ErrRecallRepositoryTeamMismatch
		}
	}
	for _, conflict := range recalled.EvidenceConflicts {
		if conflict.TeamID != expectedTeamID {
			return ErrRecallRepositoryTeamMismatch
		}
		if conflict.Kind != "evidence_conflict" {
			return ErrRecallRepositoryTeamMismatch
		}
	}
	return nil
}
