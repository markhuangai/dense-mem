package neo4j

import (
	"context"
	"errors"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// ScopedReader defines the interface for profile-scoped read operations.
type ScopedReader interface {
	ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error)
}

// ScopedWriter defines the interface for profile-scoped write operations.
type ScopedWriter interface {
	ScopedWrite(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, error)
}

// ScopedTx is the query-execution surface of a profile-scoped managed transaction.
// Consumers call RunScoped rather than tx.Run directly so every statement in a
// multi-query transaction carries the $profileId guard.
// The interface is defined here so callers and tests can depend on the abstraction
// rather than the concrete neo4j driver type.
type ScopedTx interface {
	Run(ctx context.Context, cypher string, params map[string]any) (neo4j.ResultWithContext, error)
}

// ProfileScopeEnforcer composes ScopedReader, ScopedWriter, and ScopedWriteTx
// for all profile-scoped Neo4j operations.
type ProfileScopeEnforcer interface {
	ScopedReader
	ScopedWriter
	// ScopedWriteTx opens a write transaction scoped to profileID.
	// The caller-supplied fn receives the underlying neo4j.ManagedTransaction;
	// every query inside fn MUST be executed via RunScoped to maintain the guard.
	ScopedWriteTx(ctx context.Context, profileID string, fn func(tx neo4j.ManagedTransaction) error) error
}

// FragmentActiveFilter is the Cypher WHERE-clause expression that excludes
// retracted SourceFragment nodes from all read/dedupe queries. It uses
// coalesce so that legacy nodes with no status property are treated as active
// (AC-44).
const FragmentActiveFilter = "coalesce(sf.status,'active') <> 'retracted'"

// profileScopeEnforcer enforces team_id scoping on all Neo4j operations.
// It injects profileId into queries and validates that queries contain the required placeholder.
type profileScopeEnforcer struct {
	executor Neo4jClientInterface
}

// Ensure profileScopeEnforcer implements ProfileScopeEnforcer
var _ ProfileScopeEnforcer = (*profileScopeEnforcer)(nil)

// NewProfileScopeEnforcer creates a new profile scope enforcer.
func NewProfileScopeEnforcer(executor Neo4jClientInterface) ProfileScopeEnforcer {
	return &profileScopeEnforcer{
		executor: executor,
	}
}

// Errors for profile scope enforcement
var (
	ErrEmptyProfileID            = errors.New("profileID cannot be empty")
	ErrProfileParamOverride      = errors.New("caller must not provide profileId parameter; it is injected automatically")
	ErrMissingProfilePlaceholder = errors.New("query must contain $profileId placeholder")
)

// ScopedRead executes a read query with profile scoping enforcement.
// It validates the profileID, checks for placeholder presence, injects the profileId parameter,
// and routes the query through ExecuteRead.
// Returns the result summary, query results as maps, and any error.
func (e *profileScopeEnforcer) ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	preparedParams, err := e.validateAndPrepare(profileID, query, params)
	if err != nil {
		return nil, nil, err
	}

	var results []map[string]any
	var summary neo4j.ResultSummary

	_, err = e.executor.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, err := tx.Run(ctx, query, preparedParams)
		if err != nil {
			return nil, err
		}

		// Collect all records
		records, err := result.Collect(ctx)
		if err != nil {
			return nil, err
		}

		// Convert records to maps
		results = make([]map[string]any, len(records))
		for i, record := range records {
			results[i] = record.AsMap()
		}

		// Get result summary
		summary, err = result.Consume(ctx)
		if err != nil {
			return nil, err
		}

		return nil, nil
	})

	if err != nil {
		return nil, nil, err
	}

	return summary, results, nil
}

// ScopedWrite executes a write query with profile scoping enforcement.
// It validates the profileID, checks for placeholder presence, injects the profileId parameter,
// and routes the query through ExecuteWrite.
// Returns the result summary and any error.
func (e *profileScopeEnforcer) ScopedWrite(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, error) {
	preparedParams, err := e.validateAndPrepare(profileID, query, params)
	if err != nil {
		return nil, err
	}

	var summary neo4j.ResultSummary

	_, err = e.executor.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, err := tx.Run(ctx, query, preparedParams)
		if err != nil {
			return nil, err
		}

		// Get result summary
		summary, err = result.Consume(ctx)
		if err != nil {
			return nil, err
		}

		return nil, nil
	})

	if err != nil {
		return nil, err
	}

	return summary, nil
}

// ScopedWriteTx opens a profile-scoped write transaction.
// profileID must be non-empty; the executor's ExecuteWrite is called and fn
// receives the raw neo4j.ManagedTransaction. Every Cypher statement inside fn
// must be run through RunScoped to enforce the $profileId guard per query.
// Any error returned by fn is propagated as the return value of ScopedWriteTx.
func (e *profileScopeEnforcer) ScopedWriteTx(ctx context.Context, profileID string, fn func(tx neo4j.ManagedTransaction) error) error {
	if profileID == "" {
		return ErrEmptyProfileID
	}

	_, err := e.executor.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		return nil, fn(tx)
	})
	return err
}

// RunScoped validates profile scoping and executes a single Cypher query within
// an existing managed transaction, reusing validateAndPrepare semantics:
//   - profileID must be non-empty
//   - params must not already contain "profileId"
//   - query must contain the "$profileId" placeholder
//
// profileId is injected automatically; the caller must never supply it.
// Returns the raw neo4j.ResultWithContext so callers can collect records inside a multi-query tx.
func RunScoped(ctx context.Context, tx neo4j.ManagedTransaction, profileID string, query string, params map[string]any) (neo4j.ResultWithContext, error) {
	prepared, err := (&profileScopeEnforcer{}).validateAndPrepare(profileID, query, params)
	if err != nil {
		return nil, err
	}
	return tx.Run(ctx, query, prepared)
}

// validateAndPrepare performs common validation and parameter injection.
// It returns the prepared params map with profileId injected, or an error if:
// - profileID is empty
// - params already contains "profileId" (caller override attempt)
// - query does not contain "$profileId" placeholder
// The returned map is either the original params (modified) or a new map if params was nil.
func (e *profileScopeEnforcer) validateAndPrepare(profileID string, query string, params map[string]any) (map[string]any, error) {
	// Reject empty profileID
	if profileID == "" {
		return nil, ErrEmptyProfileID
	}

	// Reject if caller tries to override profileId parameter
	if params != nil {
		if _, exists := params["profileId"]; exists {
			return nil, ErrProfileParamOverride
		}
	}

	// Reject if query doesn't contain $profileId placeholder
	if !strings.Contains(query, "$profileId") {
		return nil, ErrMissingProfilePlaceholder
	}

	// Create new map if params is nil, otherwise use existing
	prepared := params
	if prepared == nil {
		prepared = make(map[string]any)
	}

	// Inject profileId into params
	prepared["profileId"] = profileID

	return prepared, nil
}
