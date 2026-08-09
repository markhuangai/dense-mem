package memoryservice

import (
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
)

var ErrRecallRepositoryTeamMismatch = errors.New("recall: repository returned a mismatched team")

type RecallConflictPosition struct {
	PositionID              string                    `json:"position_id"`
	Disposition             string                    `json:"disposition"`
	SupporterCount          int                       `json:"supporter_count"`
	SupportGroupCount       int                       `json:"support_group_count"`
	AuthoritativeGroupCount int                       `json:"authoritative_group_count"`
	SupportersTruncated     bool                      `json:"supporters_truncated"`
	Supporters              []RecallConflictSupporter `json:"supporters"`
	RelationshipIDs         []string                  `json:"relationship_ids"`
	OwnerProfileIDs         []string                  `json:"owner_profile_ids"`
	ResultEvidenceIDs       []string                  `json:"result_evidence_ids"`
}

type RecallConflictSupporter struct {
	ProfileID          string    `json:"profile_id"`
	ProfileName        string    `json:"profile_name"`
	StrongestAuthority string    `json:"strongest_authority"`
	EvidenceID         string    `json:"evidence_id"`
	AcceptedAt         time.Time `json:"accepted_at"`
	SourceGroupCount   int       `json:"source_group_count"`
}

func validateRecallConflictTeams(recalled *repository.RecallEvidenceResult, expectedTeamID string) error {
	if recalled == nil {
		return nil
	}
	for _, conflict := range recalled.Conflicts {
		if conflict.TeamID != expectedTeamID {
			return ErrRecallRepositoryTeamMismatch
		}
	}
	return nil
}
