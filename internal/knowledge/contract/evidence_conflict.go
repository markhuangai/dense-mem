package contract

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

const (
	EvidenceConflictDefaultLimit      = 25
	EvidenceConflictMaxLimit          = 100
	EvidenceConflictDefaultEventLimit = 50
	EvidenceConflictMaxEventLimit     = 100
	EvidenceConflictMaxResults        = 20
	EvidenceConflictMaxPositions      = 10
	EvidenceConflictMaxQuoteRunes     = 4000
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

func EncodeEvidenceConflictCursor(cursor EvidenceConflictCursor) (string, error) {
	if err := cursor.Validate(cursor.TeamID, cursor.StatusFilter); err != nil {
		return "", err
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode evidence conflict cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func DecodeEvidenceConflictCursor(raw string) (*EvidenceConflictCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 1024 {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(payload) == 0 {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	var cursor EvidenceConflictCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if err := cursor.Validate(cursor.TeamID, cursor.StatusFilter); err != nil {
		return nil, err
	}
	return &cursor, nil
}

func EncodeEvidenceConflictEventCursor(cursor EvidenceConflictEventCursor) (string, error) {
	if err := cursor.Validate(cursor.TeamID, cursor.ConflictID); err != nil {
		return "", err
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode evidence conflict event cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func DecodeEvidenceConflictEventCursor(raw string) (*EvidenceConflictEventCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 1024 {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(payload) == 0 {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	var cursor EvidenceConflictEventCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, ErrEvidenceConflictInvalidCommand
	}
	if err := cursor.Validate(cursor.TeamID, cursor.ConflictID); err != nil {
		return nil, err
	}
	return &cursor, nil
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
