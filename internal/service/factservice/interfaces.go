// Package factservice provides fact promotion, retrieval, and listing
// for the knowledge-pipeline fact stage.
//
// Profile isolation invariant: every method on every service interface in this
// package receives profileID as an explicit parameter. Implementations MUST
// use that value to scope all database queries — no cross-profile reads or
// writes are permitted.
package factservice

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// PromoteClaimService defines the interface for promoting a validated Claim to a Fact.
//
// A Claim must have EntailmentVerdict == VerdictEntailed and Status == StatusValidated
// before it is eligible for promotion. Implementations are responsible for
// persisting the new Fact node to the graph, creating the PROMOTES_TO edge,
// and transitioning the Claim status to StatusSuperseded when appropriate.
type PromoteClaimService interface {
	// Promote creates a Fact from the validated Claim identified by claimID
	// within profileID. Returns the newly created Fact on success.
	// Returns an error if the Claim is not in a promotable state or does not
	// belong to profileID.
	Promote(ctx context.Context, profileID string, claimID string) (*domain.Fact, error)
}

// ConfirmMemoryRequest describes an explicit user decision for a disputed
// memory claim.
type ConfirmMemoryRequest struct {
	ClaimID  string
	Decision string
}

// ConfirmMemoryResult contains the outcome of applying a user clarification.
type ConfirmMemoryResult struct {
	ClaimID  string       `json:"claim_id"`
	Decision string       `json:"decision"`
	Status   string       `json:"status"`
	Fact     *domain.Fact `json:"fact,omitempty"`
}

// ConfirmMemoryService applies explicit user decisions to disputed claims.
type ConfirmMemoryService interface {
	ConfirmMemory(ctx context.Context, profileID string, req ConfirmMemoryRequest) (*ConfirmMemoryResult, error)
}

// GetFactService defines the interface for fact retrieval by ID.
type GetFactService interface {
	// Get retrieves the Fact identified by factID within profileID.
	// Returns a not-found error when the Fact does not exist or belongs to a
	// different profile.
	Get(ctx context.Context, profileID string, factID string) (*domain.Fact, error)
}

// ListFactsService defines the interface for paginated fact listing with
// optional filters and keyset (cursor) pagination.
type ListFactsService interface {
	// List returns up to limit Facts for profileID that match filters, ordered
	// by (recorded_at DESC, fact_id DESC).
	//
	// cursor is the pagination token returned by a previous call (empty string
	// for the first page). Returns the matching facts and the cursor to pass
	// on the next call (empty string when no further results exist).
	List(ctx context.Context, profileID string, filters FactListFilters, limit int, cursor string) ([]*domain.Fact, string, error)
}

// RetractFactService soft-tombstones a fact without changing its truth-validity
// interval. Implementations must enforce owner-scoped mutation.
type RetractFactService interface {
	// Retract marks the Fact as retracted within profileID. Missing or
	// cross-profile facts return ErrFactNotFound.
	Retract(ctx context.Context, profileID, factID string) error
}
