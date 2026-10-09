package contract

import (
	"context"
	"time"
)

type ExportPosition struct {
	XID       string    `json:"xid"`
	Timestamp time.Time `json:"timestamp"`
	ID        string    `json:"id"`
}

type ExportEntry struct {
	Position   ExportPosition
	TeamID     *string
	Operation  string
	EntityType string
	EntityID   string
	ActorID    *string
	ActorRole  string
}

type ExportRead struct {
	Entries  []ExportEntry
	Frontier string
	Delayed  bool
}

type ExportStore interface {
	ReadExport(context.Context, string, *ExportPosition, int) (ExportRead, error)
}
