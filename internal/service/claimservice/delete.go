package claimservice

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// deleteClaimServiceImpl implements DeleteClaimService.
type deleteClaimServiceImpl struct {
	writer claimWriter
	audit  AuditEmitter
	logger *slog.Logger
}

// Compile-time check that deleteClaimServiceImpl satisfies DeleteClaimService.
var _ DeleteClaimService = (*deleteClaimServiceImpl)(nil)

// NewDeleteClaimService constructs a ready-to-use DeleteClaimService.
//
// audit and logger may be nil; audit failures are swallowed so the primary
// operation always succeeds, and an absent logger emits no structured log lines.
func NewDeleteClaimService(
	writer claimWriter,
	audit AuditEmitter,
	logger *slog.Logger,
) DeleteClaimService {
	return &deleteClaimServiceImpl{
		writer: writer,
		audit:  audit,
		logger: logger,
	}
}

// deleteClaimCypher removes a Claim node and all its relationships atomically.
//
// DETACH DELETE removes the Claim node and all attached relationships —
// including any SUPPORTED_BY edges — in a single write. It does NOT cascade to
// connected SourceFragment or Fact nodes; those remain intact. Promoted facts
// retain their promoted_from_claim_id property after the originating claim is
// removed.
//
// Profile isolation: $profileId is injected automatically by ScopedWrite and
// appears in the Claim node pattern. A claim belonging to a different profile
// will not be matched, and the caller receives ErrClaimNotFound — existence
// under other profiles is never leaked.
//
// Callers MUST NOT include profileId in the params map; ScopedWrite injects it.
const deleteClaimCypher = `
MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
DETACH DELETE c`

// Delete permanently removes the claim identified by claimID from the graph.
//
// Returns ErrClaimNotFound when:
//   - the claim does not exist for profileID
//   - the claim exists but belongs to a different profile (indistinguishable from
//     absent — no existence leak)
func (s *deleteClaimServiceImpl) Delete(ctx context.Context, profileID string, claimID string) error {
	summary, err := s.writer.ScopedWrite(ctx, profileID, deleteClaimCypher, map[string]any{
		"claimId": claimID,
	})
	if err != nil {
		return fmt.Errorf("claim delete: %w", err)
	}

	// A nil summary or zero NodesDeleted means the MATCH found nothing — the
	// claim does not exist for this profile.
	if summary == nil || summary.Counters().NodesDeleted() == 0 {
		return ErrClaimNotFound
	}

	// Emit audit event; swallow failures so the primary operation succeeds.
	if s.audit != nil {
		now := time.Now().UTC()
		entry := AuditLogEntry{
			ProfileID:  profileID,
			Timestamp:  now,
			Operation:  "claim.delete",
			EntityType: "claim",
			EntityID:   claimID,
			BeforePayload: map[string]any{
				"claim_id": claimID,
				"team_id":  profileID,
			},
		}
		if auditErr := s.audit.Append(ctx, entry); auditErr != nil && s.logger != nil {
			s.logger.Warn("audit emit failed for claim.delete",
				slog.String("team_id", profileID),
				slog.String("claim_id", claimID),
				slog.String("error", auditErr.Error()),
			)
		}
	}

	return nil
}
