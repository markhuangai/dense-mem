package contract

import (
	"strings"
	"time"

	"github.com/google/uuid"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

type EvidenceConflictPositionRecord = recallcontract.EvidenceConflictPositionRecord
type EvidenceConflictEventRecord = recallcontract.EvidenceConflictEventRecord
type EvidenceConflictCaseRecord = recallcontract.EvidenceConflictCaseRecord

type EvidenceConflictListInput struct {
	TeamID string
	Status string
	Limit  int
	Cursor *EvidenceConflictCursor
}

type EvidenceConflictListResult struct {
	Items      []EvidenceConflictCaseRecord
	NextCursor *EvidenceConflictCursor
}

type EvidenceConflictCursor struct {
	Version      int       `json:"version"`
	TeamID       string    `json:"team_id"`
	StatusFilter string    `json:"status_filter"`
	UpdatedAt    time.Time `json:"updated_at"`
	ConflictID   string    `json:"conflict_id"`
}

type EvidenceConflictGetInput struct {
	TeamID      string
	ConflictID  string
	EventLimit  int
	EventCursor *EvidenceConflictEventCursor
}

type EvidenceConflictEventCursor struct {
	Version    int    `json:"version"`
	TeamID     string `json:"team_id"`
	ConflictID string `json:"conflict_id"`
	Ordinal    int64  `json:"ordinal"`
	EventID    string `json:"event_id"`
}

type EvidenceConflictGetResult struct {
	Conflict        *EvidenceConflictCaseRecord
	NextEventCursor *EvidenceConflictEventCursor
}

type EvidenceConflictResolutionInput struct {
	TeamID              string
	ConflictID          string
	ExpectedVersion     int
	Decision            string
	Reason              string
	PreferredPositionID string
	ActorKind           string
	ActorID             string
}

func (c EvidenceConflictCursor) Validate(teamID, status string) error {
	if c.Version != 1 || c.UpdatedAt.IsZero() {
		return ErrEvidenceConflictInvalidCommand
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.TeamID)); err != nil {
		return ErrEvidenceConflictInvalidCommand
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.ConflictID)); err != nil {
		return ErrEvidenceConflictInvalidCommand
	}
	if c.StatusFilter != "" && !validEvidenceConflictStatus(c.StatusFilter) {
		return ErrEvidenceConflictInvalidCommand
	}
	if strings.TrimSpace(teamID) != "" && c.TeamID != strings.TrimSpace(teamID) {
		return ErrEvidenceConflictInvalidCommand
	}
	if c.StatusFilter != strings.TrimSpace(status) {
		return ErrEvidenceConflictInvalidCommand
	}
	return nil
}

func (c EvidenceConflictEventCursor) Validate(teamID, conflictID string) error {
	if c.Version != 1 || c.Ordinal < 1 {
		return ErrEvidenceConflictInvalidCommand
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.TeamID)); err != nil {
		return ErrEvidenceConflictInvalidCommand
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.ConflictID)); err != nil {
		return ErrEvidenceConflictInvalidCommand
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.EventID)); err != nil {
		return ErrEvidenceConflictInvalidCommand
	}
	if strings.TrimSpace(teamID) != "" && c.TeamID != strings.TrimSpace(teamID) {
		return ErrEvidenceConflictInvalidCommand
	}
	if strings.TrimSpace(conflictID) != "" && c.ConflictID != strings.TrimSpace(conflictID) {
		return ErrEvidenceConflictInvalidCommand
	}
	return nil
}

func validEvidenceConflictStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "open", "resolved", "dismissed":
		return true
	default:
		return false
	}
}
