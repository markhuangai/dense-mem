package domain

import (
	"time"

	"github.com/google/uuid"
)

// OperationLog is one structured application log record persisted for control
// portal inspection.
type OperationLog struct {
	ID            uuid.UUID      `json:"id"`
	Timestamp     time.Time      `json:"timestamp"`
	Severity      string         `json:"severity"`
	SeverityRank  int            `json:"severity_rank"`
	Message       string         `json:"message"`
	Source        string         `json:"source,omitempty"`
	TeamID        *uuid.UUID     `json:"team_id,omitempty"`
	ProfileID     *uuid.UUID     `json:"profile_id,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	Error         string         `json:"error,omitempty"`
	Attrs         map[string]any `json:"attrs,omitempty"`
}

// OperationLogFilter controls control-portal log listing.
type OperationLogFilter struct {
	Limit     int
	Offset    int
	Severity  string
	Sort      string
	Direction string
}

// OperationLogPage is a paginated operation log response.
type OperationLogPage struct {
	Items []OperationLog
	Total int64
}
