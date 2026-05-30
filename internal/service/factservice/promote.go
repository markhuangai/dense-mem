package factservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	classificationSvc "github.com/markhuangai/dense-mem/internal/service/classification"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
	neo4jstorage "github.com/markhuangai/dense-mem/internal/storage/neo4j"
	postgresstorage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"gorm.io/gorm"
)

// AuditLogEntry is a local representation of an audit event emitted by the
// fact service layer. Defined locally to prevent an import cycle with the
// top-level service package.
//
// Fields mirror the audit_log table schema. Nullable columns are represented
// as pointer types so callers can distinguish absent values from zero values.
type AuditLogEntry struct {
	ProfileID     string
	Timestamp     time.Time
	Operation     string
	EntityType    string
	EntityID      string
	BeforePayload map[string]any
	AfterPayload  map[string]any
	ActorKeyID    string
	ActorRole     string
	ClientIP      string
	CorrelationID string
	Metadata      map[string]any
}

// AuditEmitter defines the minimal interface for emitting fact-related audit
// events. Defined locally to avoid an import cycle with the top-level service
// package.
//
// Implementations must be safe for concurrent use and must not log secrets.
type AuditEmitter interface {
	// Append writes a single audit entry. Implementations must not return an
	// error that causes a fact operation to fail — audit failures should be
	// logged and swallowed so the primary operation succeeds.
	Append(ctx context.Context, entry AuditLogEntry) error
}

// promoteDB is the minimal Neo4j interface required by promoteClaimServiceImpl.
//
// Profile isolation invariant: ScopedRead injects $profileId into every query.
// ScopedWriteTx requires callers to inject $profileId via RunScoped for every
// Cypher statement executed within the managed transaction.
type promoteDB interface {
	// ScopedRead executes a read query scoped to profileID and returns all
	// result rows. The $profileId placeholder is injected automatically.
	ScopedRead(
		ctx context.Context,
		profileID string,
		query string,
		params map[string]any,
	) (neo4j.ResultSummary, []map[string]any, error)

	// ScopedWriteTx opens a managed write transaction scoped to profileID.
	// Every Cypher call inside fn MUST use neo4jstorage.RunScoped to carry
	// the $profileId guard.
	ScopedWriteTx(
		ctx context.Context,
		profileID string,
		fn func(tx neo4j.ManagedTransaction) error,
	) error
}

// defaultLockTimeout is the outer deadline for acquiring the Postgres advisory
// lock and completing the promotion transaction. Sourced from
// CONFIG_PROMOTE_TX_TIMEOUT_SEC in production; overridden per-instance.
const defaultLockTimeout = 30 * time.Second

// promoteClaimServiceImpl implements PromoteClaimService.
//
// Profile isolation invariant: profileID is received as an explicit parameter
// on every method and propagated to all downstream DB and lock calls. No
// cross-profile reads or writes are permitted.
type promoteClaimServiceImpl struct {
	db          promoteDB
	locker      postgresstorage.ClaimLocker
	pgDB        *gorm.DB
	audit       AuditEmitter
	metrics     observability.DiscoverabilityMetrics
	logger      *slog.Logger
	lockTimeout time.Duration
}

// Compile-time interface satisfaction check.
var _ PromoteClaimService = (*promoteClaimServiceImpl)(nil)
var _ ConfirmMemoryService = (*promoteClaimServiceImpl)(nil)

// NewPromoteClaimService constructs a ready-to-use PromoteClaimService.
//
// audit, logger, and metrics may be nil; audit failures are logged and
// swallowed so the primary operation always succeeds. An absent logger emits
// no structured log lines; absent metrics are silently skipped.
//
// When lockTimeout is zero or negative, defaultLockTimeout (30s) is used.
func NewPromoteClaimService(
	db promoteDB,
	locker postgresstorage.ClaimLocker,
	pgDB *gorm.DB,
	audit AuditEmitter,
	logger *slog.Logger,
	metrics observability.DiscoverabilityMetrics,
	lockTimeout time.Duration,
) PromoteClaimService {
	if lockTimeout <= 0 {
		lockTimeout = defaultLockTimeout
	}
	return &promoteClaimServiceImpl{
		db:          db,
		locker:      locker,
		pgDB:        pgDB,
		audit:       audit,
		logger:      logger,
		metrics:     metrics,
		lockTimeout: lockTimeout,
	}
}

// Promote acquires a profile-scoped Postgres advisory lock on the claim, then
// executes the full promotion algorithm within that lock.
//
// The advisory lock serialises concurrent promotions of the same claimID so
// that idempotency checks and fact creation are linearised. Profile isolation
// is enforced at the lock level: profileID is embedded in the lock key so
// promotions for the same claimID in different profiles never contend.
func (s *promoteClaimServiceImpl) Promote(ctx context.Context, profileID string, claimID string) (*domain.Fact, error) {
	var fact *domain.Fact

	lockErr := s.locker.WithClaimLock(ctx, s.pgDB, profileID, claimID, s.lockTimeout, func(_ *gorm.DB) error {
		var err error
		fact, err = s.doPromote(ctx, profileID, claimID)
		return err
	})

	if lockErr != nil {
		s.incMetric("error")
		return nil, lockErr
	}
	return fact, nil
}

// ConfirmMemory applies an explicit user clarification to a disputed claim.
//
// Supported decisions:
//   - accept_claim: supersede active conflicting facts for the claim's
//     (subject, predicate), then create a new active Fact from the claim.
//   - keep_existing or reject_claim: reject the claim and keep active facts.
func (s *promoteClaimServiceImpl) ConfirmMemory(ctx context.Context, profileID string, req ConfirmMemoryRequest) (*ConfirmMemoryResult, error) {
	if req.ClaimID == "" {
		return nil, errors.New("confirm memory: claim_id is required")
	}

	var out *ConfirmMemoryResult
	lockErr := s.locker.WithClaimLock(ctx, s.pgDB, profileID, req.ClaimID, s.lockTimeout, func(_ *gorm.DB) error {
		result, err := s.doConfirmMemory(ctx, profileID, req)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	if lockErr != nil {
		s.incMetric("error")
		return nil, lockErr
	}
	return out, nil
}

func (s *promoteClaimServiceImpl) doConfirmMemory(ctx context.Context, profileID string, req ConfirmMemoryRequest) (*ConfirmMemoryResult, error) {
	claim, err := s.loadClaim(ctx, profileID, req.ClaimID)
	if err != nil {
		return nil, err
	}

	switch req.Decision {
	case "accept_claim":
		gate, ok := DefaultPromotionGates[claim.Predicate]
		if !ok {
			return nil, fmt.Errorf("%w: predicate=%s", ErrPredicateNotPoliced, claim.Predicate)
		}
		if gateErr := evaluateGates(claim, gate); gateErr != nil {
			return nil, gateErr
		}

		activeFacts, err := findActiveFactsBySubjectPredicate(ctx, s.db, profileID, claim.Subject, claim.Predicate)
		if err != nil {
			return nil, fmt.Errorf("confirm memory: find active facts: %w", err)
		}

		conflicts := make([]*domain.Fact, 0, len(activeFacts))
		for _, fact := range activeFacts {
			if fact.Object != claim.Object {
				conflicts = append(conflicts, fact)
			}
		}
		if len(conflicts) > 0 {
			if err := supersedePath(ctx, s.db, profileID, conflicts, claim.ClaimID, claim.ValidFrom); err != nil {
				return nil, fmt.Errorf("confirm memory: supersede conflicts: %w", err)
			}
		}

		fact, err := s.createNewFact(ctx, profileID, claim, gate)
		if err != nil {
			return nil, err
		}
		s.incMetric("promoted")
		s.emitAudit(ctx, profileID, claim.ClaimID, fact.FactID, "claim.confirm.accept")
		return &ConfirmMemoryResult{
			ClaimID:  claim.ClaimID,
			Decision: req.Decision,
			Status:   "accepted",
			Fact:     fact,
		}, nil
	case "keep_existing", "reject_claim":
		if err := weakerPath(ctx, s.db, profileID, claim.ClaimID); err != nil {
			return nil, fmt.Errorf("confirm memory: reject claim: %w", err)
		}
		s.emitAudit(ctx, profileID, claim.ClaimID, "", "claim.confirm.reject")
		return &ConfirmMemoryResult{
			ClaimID:  claim.ClaimID,
			Decision: req.Decision,
			Status:   "rejected",
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrInvalidConfirmationDecision, req.Decision)
	}
}

// doPromote executes the promotion algorithm. It must be called while holding
// the advisory lock for (profileID, claimID).
//
// Algorithm:
//  1. Load claim; error if absent or belonging to a different profile.
//  2. Assert status == StatusValidated → ErrClaimNotValidated.
//  3. Look up predicate gate → ErrPredicateNotPoliced for unknown predicates.
//  4. Assert policy is one implemented by the promotion service.
//  5. Evaluate all gate thresholds cumulatively → ErrGateRejected.
//  6. Idempotency: return existing Fact if PROMOTES_TO edge already exists.
//  7. Dispatch to policy-specific path (multi_valued or single_current).
//  8. Emit promotion_outcome_total metric and audit event.
func (s *promoteClaimServiceImpl) doPromote(ctx context.Context, profileID, claimID string) (*domain.Fact, error) {
	// Step 1: load the claim.
	//
	// Profile isolation: loadClaimForPromoteCypher includes {team_id: $profileId}
	// on the MATCH clause. A claim in a different profile produces zero rows,
	// indistinguishable from "not found" — no existence leak.
	claim, err := s.loadClaim(ctx, profileID, claimID)
	if err != nil {
		return nil, err
	}

	// Step 2: assert the claim is in a promotable state.
	if claim.Status != domain.StatusValidated {
		return nil, fmt.Errorf("%w: status=%s", ErrClaimNotValidated, claim.Status)
	}

	// Step 3: look up the predicate gate — deny by default (AC-34).
	gate, ok := DefaultPromotionGates[claim.Predicate]
	if !ok {
		return nil, fmt.Errorf("%w: predicate=%s", ErrPredicateNotPoliced, claim.Predicate)
	}

	// Step 4: reject unknown policies defensively.
	switch gate.Policy {
	case MultiValued, SingleCurrent, Versioned, AppendOnly:
	default:
		return nil, fmt.Errorf("%w: policy=%s", ErrUnsupportedPolicy, gate.Policy)
	}

	// Step 5: evaluate all gate thresholds cumulatively.
	if gateErr := evaluateGates(claim, gate); gateErr != nil {
		return nil, gateErr
	}

	// Step 6: idempotency — if the claim already has a PROMOTES_TO edge to a
	// Fact scoped to this profile, return the existing Fact without re-promoting.
	existing, err := s.checkIdempotency(ctx, profileID, claimID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		s.incMetric("skipped")
		s.emitAudit(ctx, profileID, claimID, existing.FactID, "claim.promote.idempotent")
		return existing, nil
	}

	// Step 7: dispatch to the policy-specific promotion path.
	var fact *domain.Fact
	switch gate.Policy {
	case MultiValued:
		// No contradiction resolution: multiple active Facts may coexist for the
		// same (subject, predicate) pair. Create a new active Fact directly.
		fact, err = s.createNewFact(ctx, profileID, claim, gate)
		if err != nil {
			return nil, err
		}
	case SingleCurrent:
		fact, err = s.handleSingleCurrent(ctx, profileID, claim, gate)
		if err != nil {
			return nil, err
		}
	case Versioned:
		fact, err = s.handleVersioned(ctx, profileID, claim, gate)
		if err != nil {
			return nil, err
		}
	case AppendOnly:
		fact, err = s.createNewFact(ctx, profileID, claim, gate)
		if err != nil {
			return nil, err
		}
	}

	// Step 8: emit metric and audit for the successful promotion path.
	s.incMetric("promoted")
	s.emitAudit(ctx, profileID, claimID, fact.FactID, "claim.promote")
	return fact, nil
}

// handleSingleCurrent executes the single_current contradiction resolution
// decision tree:
//
//  1. No active facts for (subject, predicate) → create new Fact (newActivePath).
//  2. All existing active facts have the same object as the claim → confirm
//     existing (update last_confirmed_at), link claim via PROMOTES_TO, mark
//     claim superseded, return confirmed Fact.
//  3. At least one existing active fact has a different object:
//     a. Claim weaker than any → weakerPath → ErrPromotionRejected.
//     b. Claim comparable to any → comparablePath → ErrPromotionDeferredDisputed.
//     c. Claim stronger than all → supersedePath → create new Fact.
func (s *promoteClaimServiceImpl) handleSingleCurrent(
	ctx context.Context,
	profileID string,
	claim *domain.Claim,
	gate PromotionGate,
) (*domain.Fact, error) {
	// findActiveFactsBySubjectPredicate satisfies factReader via the promoteDB
	// ScopedRead method. Profile isolation is enforced inside the helper.
	activeFacts, err := findActiveFactsBySubjectPredicate(ctx, s.db, profileID, claim.Subject, claim.Predicate)
	if err != nil {
		return nil, fmt.Errorf("promote: find active facts: %w", err)
	}

	if len(activeFacts) == 0 {
		// No competing facts — named path for readability.
		newActivePath()
		return s.createNewFact(ctx, profileID, claim, gate)
	}

	// Partition into same-object and differing-object active facts.
	var sameObj []*domain.Fact
	var differingObj []*domain.Fact
	for _, f := range activeFacts {
		if f.Object == claim.Object {
			sameObj = append(sameObj, f)
		} else {
			differingObj = append(differingObj, f)
		}
	}

	if len(differingObj) == 0 {
		// All existing active facts confirm the same object — no contradiction.
		// Update last_confirmed_at on each, then link the claim to the primary
		// (first) fact via PROMOTES_TO and mark the claim superseded.
		if err := sameObjectConfirmPath(ctx, s.db, profileID, sameObj); err != nil {
			return nil, fmt.Errorf("promote: same-object confirm: %w", err)
		}
		if err := s.linkClaimToExistingFact(ctx, profileID, claim.ClaimID, sameObj[0].FactID); err != nil {
			return nil, fmt.Errorf("promote: link claim to existing fact: %w", err)
		}
		return sameObj[0], nil
	}

	// At least one existing active fact has a different object — contradiction.
	//
	// Claim-vs-Fact comparison uses TruthScore directly because FactStrength
	// zeroes out ResolutionConf and ExtractConf (the individual confidence values
	// are not retained after promotion). Using the full CompareStrength comparison
	// vector would always return StrengthIncomparable for claim-vs-fact pairs,
	// making the weaker and supersede paths unreachable.
	claimTruth := ClaimStrength(claim, gate).TruthScore

	// Classify each differing fact against the claim's truth score.
	var weakerThanFacts []*domain.Fact // facts with higher TruthScore than claim (claim is weaker)
	var comparableFacts []*domain.Fact // facts with TruthScore within epsilon of claim

	for _, f := range differingObj {
		diff := claimTruth - f.TruthScore
		switch {
		case diff < -strengthEpsilon:
			// Existing fact is stronger than the claim.
			weakerThanFacts = append(weakerThanFacts, f)
		case math.Abs(diff) <= strengthEpsilon:
			// Scores are within epsilon — defer to human review.
			comparableFacts = append(comparableFacts, f)
			// else: claim TruthScore > fact TruthScore — fact will be superseded.
		}
	}

	// If the claim is weaker than any existing fact, reject it outright.
	if len(weakerThanFacts) > 0 {
		if err := weakerPath(ctx, s.db, profileID, claim.ClaimID); err != nil {
			return nil, fmt.Errorf("promote: weaker path: %w", err)
		}
		return nil, ErrPromotionRejected
	}

	// If the claim is comparable to any existing fact, defer to human review.
	if len(comparableFacts) > 0 {
		if err := comparablePath(ctx, s.db, profileID, claim.ClaimID, comparableFacts); err != nil {
			return nil, fmt.Errorf("promote: comparable path: %w", err)
		}
		return nil, ErrPromotionDeferredDisputed
	}

	// Claim is strictly stronger than all differing facts — supersede them all
	// and create a new active Fact.
	if err := supersedePath(ctx, s.db, profileID, differingObj, claim.ClaimID, claim.ValidFrom); err != nil {
		return nil, fmt.Errorf("promote: supersede path: %w", err)
	}
	return s.createNewFact(ctx, profileID, claim, gate)
}

// handleVersioned creates a new fact version and closes any older active
// versions for the same subject/predicate pair without running contradiction
// strength comparisons. This is intended for temporally evolving knowledge.
func (s *promoteClaimServiceImpl) handleVersioned(
	ctx context.Context,
	profileID string,
	claim *domain.Claim,
	gate PromotionGate,
) (*domain.Fact, error) {
	activeFacts, err := findActiveFactsBySubjectPredicate(ctx, s.db, profileID, claim.Subject, claim.Predicate)
	if err != nil {
		return nil, fmt.Errorf("promote: find active versioned facts: %w", err)
	}
	if len(activeFacts) > 0 {
		if err := supersedePath(ctx, s.db, profileID, activeFacts, claim.ClaimID, claim.ValidFrom); err != nil {
			return nil, fmt.Errorf("promote: versioned supersede path: %w", err)
		}
	}
	return s.createNewFact(ctx, profileID, claim, gate)
}

// createNewFact builds a new domain.Fact in memory, persists it to the graph
// together with the PROMOTES_TO edge from the Claim, and marks the Claim as
// StatusSuperseded — all within a single ScopedWriteTx for atomicity.
//
// Classification is normalised through DefaultLattice before storage so the
// fact always carries a complete, well-ordered label map (AC-37, AC-39).
//
// Profile isolation: every RunScoped call inside the transaction injects
// $profileId into the Cypher.
func (s *promoteClaimServiceImpl) createNewFact(
	ctx context.Context,
	profileID string,
	claim *domain.Claim,
	gate PromotionGate,
) (*domain.Fact, error) {
	claimStr := ClaimStrength(claim, gate)

	// Normalise classification through the DefaultLattice so the stored map
	// is always complete for all known dimensions and uses canonical values.
	lat := classificationSvc.DefaultLattice()
	mergedClass := fromStringMap(lat.Max(toStringMap(claim.Classification), map[string]string{}))

	now := time.Now().UTC()
	promoterID := ""
	promoterName := ""
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok {
		promoterID = actor.ProfileID.String()
		promoterName = actor.ProfileName
	}

	fact := &domain.Fact{
		FactID:                       uuid.New().String(),
		ProfileID:                    profileID,
		CreatedByProfileID:           claim.CreatedByProfileID,
		CreatedByProfileName:         claim.CreatedByProfileName,
		PromotedByProfileID:          promoterID,
		PromotedByProfileName:        promoterName,
		Subject:                      claim.Subject,
		Predicate:                    claim.Predicate,
		Object:                       claim.Object,
		Status:                       domain.FactStatusActive,
		TruthScore:                   claimStr.TruthScore,
		ValidFrom:                    claim.ValidFrom,
		ValidTo:                      claim.ValidTo,
		RecordedAt:                   now,
		PromotedFromClaimID:          claim.ClaimID,
		Classification:               mergedClass,
		ClassificationLatticeVersion: classificationSvc.LatticeVersion,
		SourceQuality:                claim.SourceQuality,
	}
	classificationJSON, err := fragmentcodec.EncodeOptionalMap(fact.Classification)
	if err != nil {
		return nil, fmt.Errorf("promote: encode fact classification: %w", err)
	}

	err = s.db.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		result, err := neo4jstorage.RunScoped(ctx, tx, profileID,
			createFactAndEdgeCypher,
			map[string]any{
				"claimId":                      claim.ClaimID,
				"factId":                       fact.FactID,
				"createdByProfileId":           fact.CreatedByProfileID,
				"createdByProfileName":         fact.CreatedByProfileName,
				"promotedByProfileId":          fact.PromotedByProfileID,
				"promotedByProfileName":        fact.PromotedByProfileName,
				"subject":                      fact.Subject,
				"predicate":                    fact.Predicate,
				"object":                       fact.Object,
				"status":                       string(fact.Status),
				"truthScore":                   fact.TruthScore,
				"validFrom":                    fact.ValidFrom,
				"validTo":                      fact.ValidTo,
				"recordedAt":                   fact.RecordedAt,
				"promotedFromClaimId":          fact.PromotedFromClaimID,
				"classificationJSON":           classificationJSON,
				"classificationLatticeVersion": fact.ClassificationLatticeVersion,
				"sourceQuality":                fact.SourceQuality,
				"claimStatus":                  string(domain.StatusSuperseded),
			},
		)
		if err != nil {
			return fmt.Errorf("create fact node: %w", err)
		}
		_, err = result.Consume(ctx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("promote: persist new fact: %w", err)
	}
	return fact, nil
}

// linkClaimToExistingFact creates the PROMOTES_TO relationship from the Claim
// to an already-existing Fact node, and marks the Claim as StatusSuperseded.
// Used for the same-object confirm path (AC-37) where no new Fact is created.
//
// Profile isolation: RunScoped injects $profileId into the Cypher.
func (s *promoteClaimServiceImpl) linkClaimToExistingFact(
	ctx context.Context,
	profileID, claimID, factID string,
) error {
	return s.db.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		result, err := neo4jstorage.RunScoped(ctx, tx, profileID,
			`MATCH (c:Claim {team_id: $profileId, claim_id: $claimId}),
                   (f:Fact  {team_id: $profileId, fact_id:  $factId})
             CREATE (c)-[:PROMOTES_TO {team_id: $profileId}]->(f)
             SET c.status = $claimStatus`,
			map[string]any{
				"claimId":     claimID,
				"factId":      factID,
				"claimStatus": string(domain.StatusSuperseded),
			},
		)
		if err != nil {
			return fmt.Errorf("link claim to existing fact: %w", err)
		}
		_, err = result.Consume(ctx)
		return err
	})
}

// loadClaim retrieves a Claim from Neo4j, including its supporting fragment
// IDs (needed for the support gate OR check). Returns ErrClaimNotFound when
// the Claim does not exist or belongs to a different profile.
//
// Profile isolation: the Cypher MATCH includes {team_id: $profileId}; zero
// rows are indistinguishable from "not found" — no cross-profile existence leak.
func (s *promoteClaimServiceImpl) loadClaim(ctx context.Context, profileID, claimID string) (*domain.Claim, error) {
	_, rows, err := s.db.ScopedRead(ctx, profileID, loadClaimForPromoteCypher, map[string]any{
		"claimId": claimID,
	})
	if err != nil {
		return nil, fmt.Errorf("promote: load claim: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrClaimNotFound
	}
	return rowToClaimForPromote(profileID, rows[0]), nil
}

// checkIdempotency looks for an existing PROMOTES_TO edge from the Claim to a
// Fact scoped to this profile. Returns the existing Fact when one is found,
// nil when the claim has not yet been promoted.
//
// Profile isolation: the Cypher MATCH includes {team_id: $profileId} on
// both the Claim and Fact nodes and on the PROMOTES_TO relationship.
func (s *promoteClaimServiceImpl) checkIdempotency(ctx context.Context, profileID, claimID string) (*domain.Fact, error) {
	_, rows, err := s.db.ScopedRead(ctx, profileID, idempotencyCheckCypher, map[string]any{
		"claimId": claimID,
	})
	if err != nil {
		return nil, fmt.Errorf("promote: idempotency check: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rowToFact(profileID, rows[0]), nil
}

// evaluateGates runs all PromotionGate threshold checks for the given Claim
// and returns ErrGateRejected (with a detail suffix) when any check fails.
//
// Support gate semantics are OR (AC-35): the claim passes the support check
// when EITHER len(SupportedBy) >= MinSourceCount OR
// SourceQuality >= MinMaxSourceQuality. Treating these as AND is incorrect.
func evaluateGates(claim *domain.Claim, gate PromotionGate) error {
	var failed []string

	if !validConfidence(claim.ExtractConf) {
		failed = append(failed, "extract_conf")
	} else if claim.ExtractConf < gate.MinExtractConf {
		failed = append(failed, "extract_conf")
	}
	if !validConfidence(claim.ResolutionConf) {
		failed = append(failed, "resolution_conf")
	} else if claim.ResolutionConf < gate.MinResolutionConf {
		failed = append(failed, "resolution_conf")
	}
	if gate.RequiresAssertion && claim.Modality != domain.ModalityAssertion {
		failed = append(failed, "modality")
	}
	if gate.RequiresEntailed && claim.EntailmentVerdict != domain.VerdictEntailed {
		failed = append(failed, "entailment")
	}

	// Support gate: OR semantics — either sufficient fragment count OR high source quality.
	supportCountMet := len(claim.SupportedBy) >= gate.MinSourceCount
	maxQualityMet := gate.MinMaxSourceQuality > 0 && claim.SourceQuality >= gate.MinMaxSourceQuality
	if !supportCountMet && !maxQualityMet {
		failed = append(failed, "support")
	}

	if len(failed) > 0 {
		return fmt.Errorf("%w: failed_gates=%v", ErrGateRejected, failed)
	}
	return nil
}

// incMetric bumps the promotion_outcome_total counter. Nil-safe.
func (s *promoteClaimServiceImpl) incMetric(outcome string) {
	if s.metrics != nil {
		s.metrics.IncPromotionOutcome(outcome)
	}
}

// emitAudit writes a claim.promote audit entry. Any error is logged and
// swallowed so audit failures never bubble up to the caller.
func (s *promoteClaimServiceImpl) emitAudit(
	ctx context.Context,
	profileID, claimID, factID, operation string,
) {
	if s.audit == nil {
		return
	}
	entry := AuditLogEntry{
		ProfileID:  profileID,
		Timestamp:  time.Now().UTC(),
		Operation:  operation,
		EntityType: "fact",
		EntityID:   factID,
		AfterPayload: map[string]any{
			"claim_id": claimID,
			"team_id":  profileID,
			"fact_id":  factID,
		},
	}
	if auditErr := s.audit.Append(ctx, entry); auditErr != nil && s.logger != nil {
		s.logger.Warn("audit emit failed for "+operation,
			slog.String("team_id", profileID),
			slog.String("claim_id", claimID),
			slog.String("fact_id", factID),
			slog.String("error", auditErr.Error()),
		)
	}
}

// toStringMap converts a map[string]any to map[string]string, preserving only
// entries whose values are string-typed. Non-string values are silently dropped.
// Used to interface with the classification Lattice which operates on string maps.
func toStringMap(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// fromStringMap converts a map[string]string to map[string]any.
// Used to store lattice-normalised classification back on domain types.
func fromStringMap(m map[string]string) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// rowToClaimForPromote maps a Neo4j result row to the minimal domain.Claim
// fields required for promotion. profileID is propagated from the caller
// rather than read from the row (ScopedRead has already enforced isolation).
func rowToClaimForPromote(profileID string, row map[string]any) *domain.Claim {
	strVal := func(key string) string {
		v, _ := row[key].(string)
		return v
	}
	float64Val := func(key string) float64 {
		v, _ := row[key].(float64)
		return v
	}
	timePtr := func(key string) *time.Time {
		v, ok := row[key].(time.Time)
		if !ok {
			return nil
		}
		return &v
	}

	var supportedBy []string
	if raw, ok := row["supported_by"].([]any); ok {
		supportedBy = make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				supportedBy = append(supportedBy, s)
			}
		}
	}

	var classification map[string]any
	if decoded := fragmentcodec.DecodeOptionalMap(row["classification"]); decoded != nil {
		classification = decoded
	} else if decoded := fragmentcodec.DecodeOptionalMap(row["classification_json"]); decoded != nil {
		classification = decoded
	}

	return &domain.Claim{
		ClaimID:                      strVal("claim_id"),
		ProfileID:                    profileID,
		CreatedByProfileID:           strVal("created_by_profile_id"),
		CreatedByProfileName:         strVal("created_by_profile_name"),
		Subject:                      strVal("subject"),
		Predicate:                    strVal("predicate"),
		Object:                       strVal("object"),
		Modality:                     domain.ClaimModality(strVal("modality")),
		Status:                       domain.ClaimStatus(strVal("status")),
		EntailmentVerdict:            domain.EntailmentVerdict(strVal("entailment_verdict")),
		ExtractConf:                  float64Val("extract_conf"),
		ResolutionConf:               float64Val("resolution_conf"),
		SourceQuality:                float64Val("source_quality"),
		ValidFrom:                    timePtr("valid_from"),
		Classification:               classification,
		ClassificationLatticeVersion: strVal("classification_lattice_version"),
		SupportedBy:                  supportedBy,
	}
}

// ── Cypher query constants ──────────────────────────────────────────────────

// loadClaimForPromoteCypher retrieves the Claim fields needed for promotion
// evaluation, including fragment IDs collected via outgoing SUPPORTED_BY edges
// from the Claim to its SourceFragment nodes (Claim→SourceFragment, per AC-14).
//
// Profile isolation: $profileId injected by ScopedRead and present on both
// the Claim node pattern, the SourceFragment node pattern, and the SUPPORTED_BY
// relationship pattern.
const loadClaimForPromoteCypher = `
MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
OPTIONAL MATCH (c)-[:SUPPORTED_BY {team_id: $profileId}]->(sf:SourceFragment {team_id: $profileId})
RETURN
    c.claim_id                        AS claim_id,
    c.created_by_profile_id           AS created_by_profile_id,
    c.created_by_profile_name         AS created_by_profile_name,
    c.subject                         AS subject,
    c.predicate                       AS predicate,
    c.object                          AS object,
    c.modality                        AS modality,
    c.status                          AS status,
    c.entailment_verdict              AS entailment_verdict,
    c.extract_conf                    AS extract_conf,
    c.resolution_conf                 AS resolution_conf,
    c.source_quality                  AS source_quality,
    c.valid_from                      AS valid_from,
    c.classification                  AS classification,
    c.classification_json             AS classification_json,
    c.classification_lattice_version  AS classification_lattice_version,
    collect(sf.fragment_id)           AS supported_by`

// idempotencyCheckCypher returns any Fact already reached from the Claim via a
// PROMOTES_TO edge scoped to this profile. Zero rows means no prior promotion.
//
// Profile isolation: $profileId on Claim node, Fact node, and PROMOTES_TO
// relationship prevents cross-profile idempotency hits.
const idempotencyCheckCypher = `
MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})-[:PROMOTES_TO {team_id: $profileId}]->(f:Fact {team_id: $profileId})
RETURN
    f.fact_id                        AS fact_id,
    f.created_by_profile_id          AS created_by_profile_id,
    f.created_by_profile_name        AS created_by_profile_name,
    f.promoted_by_profile_id         AS promoted_by_profile_id,
    f.promoted_by_profile_name       AS promoted_by_profile_name,
    f.subject                        AS subject,
    f.predicate                      AS predicate,
    f.object                         AS object,
    f.status                         AS status,
    f.truth_score                    AS truth_score,
    f.valid_from                     AS valid_from,
    f.valid_to                       AS valid_to,
    f.recorded_at                    AS recorded_at,
    f.recorded_to                    AS recorded_to,
    f.retracted_at                   AS retracted_at,
    f.last_confirmed_at              AS last_confirmed_at,
    f.promoted_from_claim_id         AS promoted_from_claim_id,
    f.classification                 AS classification,
    f.classification_json            AS classification_json,
    f.classification_lattice_version AS classification_lattice_version,
    f.source_quality                 AS source_quality,
    f.labels                         AS labels,
    f.metadata                       AS metadata`

// createFactAndEdgeCypher atomically creates the Fact node, the PROMOTES_TO
// edge from the Claim to the new Fact, and marks the Claim as superseded —
// all within one Cypher statement so the three mutations share a single write
// transaction committed by ScopedWriteTx.
//
// Profile isolation: $profileId appears on the Claim MATCH, the new Fact node,
// and the PROMOTES_TO relationship; RunScoped also injects it automatically.
const createFactAndEdgeCypher = `
MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
CREATE (f:Fact {
    team_id:                    $profileId,
    fact_id:                       $factId,
    created_by_profile_id:         $createdByProfileId,
    created_by_profile_name:       $createdByProfileName,
    promoted_by_profile_id:        $promotedByProfileId,
    promoted_by_profile_name:      $promotedByProfileName,
    subject:                       $subject,
    predicate:                     $predicate,
    object:                        $object,
    status:                        $status,
    truth_score:                   $truthScore,
    valid_from:                    $validFrom,
    valid_to:                      $validTo,
    recorded_at:                   $recordedAt,
    promoted_from_claim_id:        $promotedFromClaimId,
    classification_json:           $classificationJSON,
    classification_lattice_version: $classificationLatticeVersion,
    source_quality:                $sourceQuality
})
CREATE (c)-[:PROMOTES_TO {team_id: $profileId}]->(f)
SET c.status = $claimStatus`
