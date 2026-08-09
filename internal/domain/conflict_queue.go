package domain

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ConflictQueueDefaultLimit  = 25
	ConflictQueueMaxLimit      = 100
	ConflictQueueMaxPositions  = 10
	ConflictQueueMaxSupporters = 5
)

var conflictQueueFailureClasses = map[string]struct{}{
	"none": {}, "dossier_invalid": {}, "dossier_bound_exceeded": {},
	"malformed_response": {}, "invalid_response": {}, "below_confidence_threshold": {},
	"timeout": {}, "rate_limited": {}, "http_4xx": {}, "http_5xx": {},
	"http_unexpected": {}, "transport": {}, "provider_protocol": {},
	"request_invalid": {}, "provider_unavailable": {}, "unknown": {},
}

func NormalizeConflictQueueFailureClass(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "none"
	}
	if _, ok := conflictQueueFailureClasses[value]; ok {
		return value
	}
	return "unknown"
}

func NormalizeConflictQueueAssessmentDecision(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "selected", "abstained", "failed":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func NormalizeConflictQueueResolutionMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "deterministic", "ai", "last_write_wins":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func NormalizeConflictQueueResolutionOutcome(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "resolved", "pending":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

var (
	ErrInvalidConflictQueueCursor = errors.New("invalid conflict queue cursor")
	ErrConflictQueueCursorScope   = errors.New("conflict queue cursor scope mismatch")
)

type ConflictQueueQuery struct {
	TeamID string
	Status string
	Limit  int
	Cursor *ConflictQueueCursor
}

type ConflictQueueCursor struct {
	Version      int       `json:"version"`
	TeamID       string    `json:"team_id"`
	StatusFilter string    `json:"status_filter"`
	Status       string    `json:"status"`
	NextReviewAt time.Time `json:"next_review_at"`
	ConflictID   string    `json:"conflict_id"`
}

func EncodeConflictQueueCursor(cursor ConflictQueueCursor) (string, error) {
	if err := cursor.ValidateScope(cursor.TeamID, cursor.StatusFilter); err != nil {
		return "", err
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode conflict queue cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func DecodeConflictQueueCursor(raw string) (*ConflictQueueCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 1024 {
		return nil, ErrInvalidConflictQueueCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(payload) == 0 {
		return nil, ErrInvalidConflictQueueCursor
	}
	var cursor ConflictQueueCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, ErrInvalidConflictQueueCursor
	}
	if err := cursor.ValidateScope(cursor.TeamID, cursor.StatusFilter); err != nil {
		return nil, err
	}
	return &cursor, nil
}

func (c ConflictQueueCursor) ValidateScope(teamID, statusFilter string) error {
	if c.Version != 1 {
		return ErrInvalidConflictQueueCursor
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.TeamID)); err != nil {
		return ErrInvalidConflictQueueCursor
	}
	if _, err := uuid.Parse(strings.TrimSpace(c.ConflictID)); err != nil {
		return ErrInvalidConflictQueueCursor
	}
	if c.Status != "open" && c.Status != "overdue" {
		return ErrInvalidConflictQueueCursor
	}
	if c.StatusFilter != "" && c.StatusFilter != "open" && c.StatusFilter != "overdue" {
		return ErrInvalidConflictQueueCursor
	}
	if c.NextReviewAt.IsZero() {
		return ErrInvalidConflictQueueCursor
	}
	if strings.TrimSpace(teamID) != "" && c.TeamID != strings.TrimSpace(teamID) {
		return ErrConflictQueueCursorScope
	}
	if c.StatusFilter != strings.TrimSpace(statusFilter) {
		return ErrConflictQueueCursorScope
	}
	return nil
}

func (c ConflictQueueCursor) StatusRank() int {
	if c.Status == "overdue" {
		return 0
	}
	return 1
}

type ConflictQueuePage struct {
	Summary    ConflictQueueSummary `json:"summary"`
	Items      []ConflictQueueItem  `json:"items"`
	NextCursor *string              `json:"next_cursor"`
}

type ConflictQueueSummary struct {
	OpenCount                int       `json:"open_count"`
	OverdueCount             int       `json:"overdue_count"`
	ActiveLeaseCount         int       `json:"active_lease_count"`
	ExpiredLeaseCount        int       `json:"expired_lease_count"`
	FailedAssessmentCount24h int       `json:"failed_assessment_count_24h"`
	LWWResolutionCount24h    int       `json:"lww_resolution_count_24h"`
	PendingDerivedTaskCount  int       `json:"pending_derived_task_count"`
	FailedDerivedTaskCount   int       `json:"failed_derived_task_count"`
	OldestOpenAgeSeconds     int64     `json:"oldest_open_age_seconds"`
	OldestOverdueAgeSeconds  int64     `json:"oldest_overdue_age_seconds"`
	CollectedAt              time.Time `json:"collected_at"`
}

type ConflictQueueItem struct {
	ConflictID       string                  `json:"conflict_id"`
	Version          int                     `json:"version"`
	Status           string                  `json:"status"`
	Question         string                  `json:"question"`
	PredicateKey     string                  `json:"predicate_key"`
	ReviewDueAt      time.Time               `json:"review_due_at"`
	NextReviewAt     time.Time               `json:"next_review_at"`
	CreatedAt        time.Time               `json:"created_at"`
	UpdatedAt        time.Time               `json:"updated_at"`
	AttemptCount     int                     `json:"attempt_count"`
	LeaseState       string                  `json:"lease_state"`
	LeaseUntil       *time.Time              `json:"lease_until,omitempty"`
	LastFailureClass string                  `json:"last_failure_class"`
	Positions        []ConflictQueuePosition `json:"positions"`
}

type ConflictQueuePosition struct {
	PositionID              string                   `json:"position_id"`
	PositionKey             string                   `json:"position_key"`
	Disposition             string                   `json:"disposition"`
	SupporterCount          int                      `json:"supporter_count"`
	SupportGroupCount       int                      `json:"support_group_count"`
	AuthoritativeGroupCount int                      `json:"authoritative_group_count"`
	SupportersTruncated     bool                     `json:"supporters_truncated"`
	Supporters              []ConflictQueueSupporter `json:"supporters"`
}

type ConflictQueueSupporter struct {
	ProfileID          string    `json:"profile_id"`
	ProfileName        string    `json:"profile_name"`
	StrongestAuthority string    `json:"strongest_authority"`
	AcceptedAt         time.Time `json:"accepted_at"`
	SourceGroupCount   int       `json:"source_group_count"`
}

type ConflictQueueMetricsSnapshot struct {
	CollectedAt  time.Time
	Cases        []ConflictQueueMetricCase
	OldestAges   []ConflictQueueMetricOldestAge
	Leases       []ConflictQueueMetricLease
	DerivedTasks []ConflictQueueMetricDerivedTask
}

type ConflictQueueMetricCase struct {
	TeamID string
	Status string
	Value  float64
}

type ConflictQueueMetricOldestAge struct {
	TeamID string
	Status string
	Value  float64
}

type ConflictQueueMetricLease struct {
	TeamID string
	State  string
	Value  float64
}

type ConflictQueueMetricDerivedTask struct {
	TeamID string
	Status string
	Value  float64
}
