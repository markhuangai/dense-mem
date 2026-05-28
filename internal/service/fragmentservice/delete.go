// Package fragmentservice — delete path.
//
// HARD-DELETE CHOICE (AC-31):
// V1 uses hard delete. When a caller deletes a fragment, the SourceFragment node is
// removed via DETACH DELETE. There is no archive/restore workflow. Audit logs already
// capture the create + delete lifecycle (AC-26, AC-54), so lineage is preserved outside
// the graph. Soft-delete / tombstoning is explicitly deferred to a future design.
package fragmentservice

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

// DeleteFragmentService hard-deletes a fragment within a profile scope.
type DeleteFragmentService interface {
	// Delete removes the fragment with the given ID in the given profile.
	// Returns ErrFragmentNotFound if the fragment does not exist or is out-of-scope
	// (the two cases are indistinguishable to the caller).
	Delete(ctx context.Context, profileID, fragmentID string) error
}

// deleteFragmentService implements DeleteFragmentService.
type deleteFragmentService struct {
	writer neo4j.ScopedWriter
	reader ScopedReader
	audit  AuditEmitter
	logger *slog.Logger
}

var _ DeleteFragmentService = (*deleteFragmentService)(nil)

// NewDeleteFragmentService constructs a DeleteFragmentService.
func NewDeleteFragmentService(writer neo4j.ScopedWriter, reader ScopedReader, audit AuditEmitter, logger *slog.Logger) DeleteFragmentService {
	return &deleteFragmentService{
		writer: writer,
		reader: reader,
		audit:  audit,
		logger: logger,
	}
}

// Delete removes the fragment and emits an audit event. See package doc for the hard-delete rationale.
func (s *deleteFragmentService) Delete(ctx context.Context, profileID, fragmentID string) error {
	// Step 1: pre-flight existence check — guarantees accurate 404 response when the
	// fragment does not exist in this profile (AC-31 no existence leak).
	existsQuery := `
		MATCH (sf:SourceFragment {team_id: $profileId, fragment_id: $fragmentId})
		RETURN sf.fragment_id AS fragment_id
		LIMIT 1
	`
	existsParams := map[string]any{"fragmentId": fragmentID}
	_, rows, err := s.reader.ScopedRead(ctx, profileID, existsQuery, existsParams)
	if err != nil {
		return fmt.Errorf("failed to check fragment existence: %w", err)
	}
	if len(rows) == 0 {
		return ErrFragmentNotFound
	}

	// Step 2: perform the hard delete via ScopedWrite.
	deleteQuery := `
		MATCH (sf:SourceFragment {team_id: $profileId, fragment_id: $fragmentId})
		DETACH DELETE sf
	`
	deleteParams := map[string]any{"fragmentId": fragmentID}
	if _, err := s.writer.ScopedWrite(ctx, profileID, deleteQuery, deleteParams); err != nil {
		return fmt.Errorf("failed to delete fragment: %w", err)
	}

	// Step 3: emit audit event (AC-26 — no content in payload).
	// CorrelationID carries the upstream request id so delete events can be traced
	// back to the originating HTTP/MCP call (AC-54).
	if s.audit != nil {
		entry := AuditLogEntry{
			ProfileID:     &profileID,
			Timestamp:     time.Now().UTC(),
			Operation:     "fragment.delete",
			EntityType:    "fragment",
			EntityID:      fragmentID,
			CorrelationID: correlation.FromContext(ctx),
			AfterPayload: map[string]interface{}{
				"fragment_id": fragmentID,
				"team_id":     profileID,
			},
		}
		if err := s.audit.Append(ctx, entry); err != nil {
			if s.logger != nil {
				s.logger.Warn("failed to emit audit event for fragment deletion",
					slog.String("team_id", profileID),
					slog.String("fragment_id", fragmentID),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	return nil
}
