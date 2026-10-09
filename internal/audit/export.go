package audit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/audit/contract"
)

const ExportMaxBytes = 1 << 20
const ExportMaxEvents = 1000
const ExportDefaultEvents = 100

var ErrInvalidExport = errors.New("invalid audit export request")
var ErrExportUnavailable = errors.New("audit export unavailable")

type ExportRequest struct {
	TeamID *uuid.UUID
	Cursor string
	Limit  int
}

type ExportEvent struct {
	Type       string    `json:"type"`
	Version    int       `json:"version"`
	ID         string    `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	TeamID     *string   `json:"team_id,omitempty"`
	Operation  string    `json:"operation"`
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id,omitempty"`
	ActorID    string    `json:"actor_id,omitempty"`
	ActorRole  string    `json:"actor_role"`
}

type ExportCheckpoint struct {
	Type         string `json:"type"`
	Version      int    `json:"version"`
	Cursor       string `json:"cursor"`
	Events       int    `json:"events"`
	More         bool   `json:"more"`
	SafeFrontier string `json:"safe_frontier"`
	Delayed      bool   `json:"delayed_by_transaction"`
}

type exportCursor struct {
	Version  int                      `json:"version"`
	Scope    string                   `json:"scope"`
	Position *contract.ExportPosition `json:"position"`
}

func parseExportRequest(request ExportRequest) (exportCursor, int, error) {
	scope := "instance"
	if request.TeamID != nil {
		if *request.TeamID == uuid.Nil {
			return exportCursor{}, 0, ErrInvalidExport
		}
		scope = request.TeamID.String()
	}
	limit := request.Limit
	if limit == 0 {
		limit = ExportDefaultEvents
	}
	if limit < 1 || limit > ExportMaxEvents || len(request.Cursor) > 1024 {
		return exportCursor{}, 0, ErrInvalidExport
	}
	cursor := exportCursor{Version: 1, Scope: scope}
	if request.Cursor == "" {
		return cursor, limit, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(request.Cursor)
	if err != nil {
		return cursor, 0, ErrInvalidExport
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var decoded exportCursor
	if err := decoder.Decode(&decoded); err != nil {
		return cursor, 0, ErrInvalidExport
	}
	cursor = decoded
	if err := decoder.Decode(new(any)); err != io.EOF || cursor.Version != 1 || cursor.Scope != scope {
		return cursor, 0, ErrInvalidExport
	}
	if pos := cursor.Position; pos != nil {
		xid, err := strconv.ParseUint(pos.XID, 10, 64)
		if err != nil || strconv.FormatUint(xid, 10) != pos.XID || pos.Timestamp.IsZero() {
			return cursor, 0, ErrInvalidExport
		}
		id, err := uuid.Parse(pos.ID)
		if err != nil || id == uuid.Nil || id.String() != pos.ID {
			return cursor, 0, ErrInvalidExport
		}
	}
	return cursor, limit, nil
}

func encodeExportCursor(cursor exportCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw), err
}

func safeExportUUID(value string) string {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return ""
	}
	return id.String()
}

func safeExportClass(value string, allowed string) string {
	for _, candidate := range strings.Fields(allowed) {
		if candidate == value {
			return value
		}
	}
	return "other"
}

func projectExportEntry(entry contract.ExportEntry) ExportEvent {
	event := ExportEvent{Type: "event", Version: 1, ID: entry.Position.ID, Timestamp: entry.Position.Timestamp,
		Operation:  safeExportClass(entry.Operation, "CREATE UPDATE DELETE DELETE_BLOCKED REVOKE ROTATE_KEY AUTH_FAILURE CROSS_PROFILE_DENIED RATE_LIMITED SYSTEM_QUERY INVARIANT_VIOLATION SECURITY_REJECTED EVALUATION_TOOL_CALL ONTOLOGY_MAINTENANCE_COMMAND"),
		EntityType: safeExportClass(entry.EntityType, "profile team api_key credential request system relationship evidence memory_space private_memory_erasure private_memory_legal_hold ontology_maintenance evaluation_tool memory_intake_attempt"),
		EntityID:   safeExportUUID(entry.EntityID), ActorRole: safeExportClass(entry.ActorRole, "admin manager member reader writer control system")}
	if entry.TeamID != nil {
		if id := safeExportUUID(*entry.TeamID); id != "" {
			event.TeamID = &id
		}
	}
	if entry.ActorID != nil {
		event.ActorID = safeExportUUID(*entry.ActorID)
	}
	return event
}

// ExportPage returns a complete bounded page so a storage failure cannot produce a successful checkpoint.
func (s *Service) ExportPage(ctx context.Context, request ExportRequest) ([]byte, error) {
	cursor, limit, err := parseExportRequest(request)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ErrExportUnavailable
	}
	store, ok := s.store.(contract.ExportStore)
	if !ok {
		return nil, ErrExportUnavailable
	}
	select {
	case s.exportSlots <- struct{}{}:
		defer func() { <-s.exportSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, ErrExportUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	team := ""
	if request.TeamID != nil {
		team = request.TeamID.String()
	}
	read, err := store.ReadExport(ctx, team, cursor.Position, limit+1)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %w", ErrExportUnavailable, err)
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	count := 0
	more := len(read.Entries) > limit
	for _, entry := range read.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if count >= limit {
			break
		}
		raw, err := json.Marshal(projectExportEntry(entry))
		if err != nil {
			return nil, err
		}
		if buffer.Len()+len(raw)+1 > ExportMaxBytes-2048 {
			more = true
			break
		}
		buffer.Write(raw)
		buffer.WriteByte('\n')
		position := entry.Position
		cursor.Position = &position
		count++
	}
	next, err := encodeExportCursor(cursor)
	if err != nil {
		return nil, err
	}
	if err := encoder.Encode(ExportCheckpoint{Type: "checkpoint", Version: 1, Cursor: next, Events: count, More: more, SafeFrontier: read.Frontier, Delayed: read.Delayed}); err != nil {
		return nil, err
	}
	if buffer.Len() > ExportMaxBytes {
		return nil, ErrExportUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
