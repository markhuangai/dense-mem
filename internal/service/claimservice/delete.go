package claimservice

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/ownership"
)

// deleteClaimServiceImpl implements DeleteClaimService.
type deleteClaimServiceImpl struct {
	writer claimWriter
	reader claimReader
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
	svc := &deleteClaimServiceImpl{
		writer: writer,
		audit:  audit,
		logger: logger,
	}
	if reader, ok := writer.(claimReader); ok {
		svc.reader = reader
	}
	return svc
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

const claimOwnerCypher = `
MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
RETURN coalesce(c.owner_profile_id, c.created_by_profile_id, '') AS owner_profile_id
LIMIT 1`

// Delete permanently removes the claim identified by claimID from the graph.
//
// Returns ErrClaimNotFound when:
//   - the claim does not exist for profileID
//   - the claim exists but belongs to a different profile (indistinguishable from
//     absent — no existence leak)
func (s *deleteClaimServiceImpl) Delete(ctx context.Context, profileID string, claimID string) error {
	if err := s.requireClaimOwner(ctx, profileID, claimID); err != nil {
		return err
	}

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

func (s *deleteClaimServiceImpl) requireClaimOwner(ctx context.Context, profileID, claimID string) error {
	if ownership.ActorOwnerID(ctx) == "" {
		return nil
	}
	if s.reader == nil {
		return ownership.ErrOwnerMismatch
	}
	_, rows, err := s.reader.ScopedRead(ctx, profileID, claimOwnerCypher, map[string]any{
		"claimId": claimID,
	})
	if err != nil {
		return fmt.Errorf("claim delete: owner check: %w", err)
	}
	if len(rows) == 0 {
		return ErrClaimNotFound
	}
	ownerID, _ := rows[0]["owner_profile_id"].(string)
	return ownership.RequireOwner(ctx, ownerID)
}
