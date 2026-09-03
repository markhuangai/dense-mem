package memoryservice

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
)

var ErrRecallRepositoryTeamMismatch = errors.New("recall: repository returned a mismatched team")

type RecallConflictPosition struct {
	PositionID          string                    `json:"position_id"`
	Disposition         string                    `json:"disposition"`
	EvidenceID          string                    `json:"evidence_id,omitempty"`
	OccurrenceID        string                    `json:"occurrence_id,omitempty"`
	Quote               string                    `json:"quote,omitempty"`
	SpanStart           int                       `json:"span_start"`
	SpanEnd             int                       `json:"span_end"`
	Authority           string                    `json:"authority,omitempty"`
	Submitted           bool                      `json:"submitted,omitempty"`
	SupporterCount      int                       `json:"supporter_count"`
	SupportersTruncated bool                      `json:"supporters_truncated"`
	Supporters          []RecallConflictSupporter `json:"supporters"`
	RelationshipIDs     []string                  `json:"relationship_ids"`
	OwnerProfileIDs     []string                  `json:"owner_profile_ids"`
	ResultEvidenceIDs   []string                  `json:"result_evidence_ids"`
}

// MarshalJSON keeps the historical Relationship-conflict position shape
// stable while exposing exact occurrence spans only for evidence conflicts.
// The parent conflict kind is the discriminator, so one shared in-process
// position type does not need to leak both branch schemas on the wire.
func (s RecallConflictSummary) MarshalJSON() ([]byte, error) {
	if s.Kind == "evidence_conflict" {
		positions := make([]evidenceConflictPositionJSON, 0, len(s.Positions))
		for _, position := range s.Positions {
			positions = append(positions, evidenceConflictPositionJSON{
				PositionID: position.PositionID, Disposition: position.Disposition,
				EvidenceID: position.EvidenceID, OccurrenceID: position.OccurrenceID,
				Quote: position.Quote, SpanStart: position.SpanStart, SpanEnd: position.SpanEnd,
				Authority: position.Authority, Submitted: position.Submitted,
			})
		}
		var preferred *string
		if s.PreferredPositionID != "" {
			value := s.PreferredPositionID
			preferred = &value
		}
		return json.Marshal(evidenceConflictSummaryJSON{
			ConflictID: s.ConflictID, Version: s.Version, Kind: s.Kind, Status: s.Status,
			PreferredPositionID: preferred, Positions: positions, PositionsTruncated: s.PositionsTruncated,
		})
	}
	positions := make([]relationshipConflictPositionJSON, 0, len(s.Positions))
	for _, position := range s.Positions {
		positions = append(positions, relationshipConflictPositionJSON{
			PositionID: position.PositionID, Disposition: position.Disposition,
			SupporterCount: position.SupporterCount, SupportersTruncated: position.SupportersTruncated,
			Supporters: position.Supporters, RelationshipIDs: position.RelationshipIDs,
			OwnerProfileIDs: position.OwnerProfileIDs, ResultEvidenceIDs: position.ResultEvidenceIDs,
		})
	}
	return json.Marshal(relationshipConflictSummaryJSON{
		ConflictID: s.ConflictID, Version: s.Version, Kind: s.Kind, Status: s.Status,
		Question: s.Question, ReviewDueAt: s.ReviewDueAt, EffectiveAt: s.EffectiveAt,
		EffectiveTimeBasis: s.EffectiveTimeBasis, PreferredPositionID: s.PreferredPositionID,
		Positions: positions, PositionsTruncated: s.PositionsTruncated,
	})
}

type relationshipConflictSummaryJSON struct {
	ConflictID          string                             `json:"conflict_id"`
	Version             int                                `json:"version"`
	Kind                string                             `json:"kind"`
	Status              string                             `json:"status"`
	Question            string                             `json:"question"`
	ReviewDueAt         *time.Time                         `json:"review_due_at"`
	EffectiveAt         *time.Time                         `json:"effective_at"`
	EffectiveTimeBasis  string                             `json:"effective_time_basis,omitempty"`
	PreferredPositionID string                             `json:"preferred_position_id,omitempty"`
	Positions           []relationshipConflictPositionJSON `json:"positions"`
	PositionsTruncated  bool                               `json:"positions_truncated"`
}

type relationshipConflictPositionJSON struct {
	PositionID          string                    `json:"position_id"`
	Disposition         string                    `json:"disposition"`
	SupporterCount      int                       `json:"supporter_count"`
	SupportersTruncated bool                      `json:"supporters_truncated"`
	Supporters          []RecallConflictSupporter `json:"supporters"`
	RelationshipIDs     []string                  `json:"relationship_ids"`
	OwnerProfileIDs     []string                  `json:"owner_profile_ids"`
	ResultEvidenceIDs   []string                  `json:"result_evidence_ids"`
}

type evidenceConflictSummaryJSON struct {
	ConflictID          string                         `json:"conflict_id"`
	Version             int                            `json:"version"`
	Kind                string                         `json:"kind"`
	Status              string                         `json:"status"`
	PreferredPositionID *string                        `json:"preferred_position_id"`
	Positions           []evidenceConflictPositionJSON `json:"positions"`
	PositionsTruncated  bool                           `json:"positions_truncated"`
}

type evidenceConflictPositionJSON struct {
	PositionID   string `json:"position_id"`
	Disposition  string `json:"disposition"`
	EvidenceID   string `json:"evidence_id"`
	OccurrenceID string `json:"occurrence_id"`
	Quote        string `json:"quote"`
	SpanStart    int    `json:"span_start"`
	SpanEnd      int    `json:"span_end"`
	Authority    string `json:"authority"`
	Submitted    bool   `json:"submitted"`
}

type RecallConflictSupporter struct {
	ProfileID          string    `json:"profile_id"`
	ProfileName        string    `json:"profile_name"`
	StrongestAuthority string    `json:"strongest_authority"`
	EvidenceID         string    `json:"evidence_id"`
	AcceptedAt         time.Time `json:"accepted_at"`
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
