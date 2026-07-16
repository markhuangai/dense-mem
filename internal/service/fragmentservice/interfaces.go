// Package fragmentservice provides fragment creation and management services.
package fragmentservice

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
)

// CreateResult contains the result of a fragment creation operation.
type CreateResult struct {
	Fragment    *domain.Fragment
	Duplicate   bool   // true when returned via idempotency/content-hash dedupe
	DuplicateOf string // fragment id of existing match when Duplicate == true
}

// CreateFragmentService defines the interface for fragment creation.
// Implementations must handle embedding generation, persistence, deduplication, and audit.
type CreateFragmentService interface {
	// Create creates a new fragment with server-side embedding.
	// Returns CreateResult with Duplicate=true if the fragment already exists
	// (via idempotency key or content hash deduplication).
	// Returns error if embedding generation fails (AC-23: no silent unembedded writes).
	Create(ctx context.Context, profileID string, req *dto.CreateFragmentRequest) (*CreateResult, error)
}

// RetractFragmentService tombstones a fragment and recomputes affected facts.
//
// RETRACT VS DELETE: Retract is a soft tombstone (status='retracted', retracted_at=now).
// The node remains in the graph so lineage is preserved, but it is excluded from all
// active-fragment reads (FragmentActiveFilter). Hard delete (DETACH DELETE) is a
// separate operation (DeleteFragmentService).
//
// FACT REVALIDATION: After tombstoning, facts whose remaining active support no longer
// satisfies the DefaultPromotionGates threshold are marked status='needs_revalidation'.
// The support gate uses OR semantics: support_count >= MinSourceCount OR
// max_source_quality >= MinMaxSourceQuality (AC-35).
type RetractFragmentService interface {
	// Retract tombstones the fragment and marks affected facts for revalidation
	// when their remaining active support falls below the promotion gate.
	// Returns ErrFragmentNotFound if the fragment does not exist in this profile.
	Retract(ctx context.Context, profileID, fragmentID string) error
}
