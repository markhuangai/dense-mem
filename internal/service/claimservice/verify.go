package claimservice

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"github.com/markhuangai/dense-mem/internal/verifier"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// verifyClaimCypher updates a Claim node's verification fields after a
// successful entailment check.
//
// Profile isolation: $profileId is injected by ScopedWrite and must match the
// Claim node's team_id. A mismatched profileId produces zero MATCH results,
// leaving the node untouched and preventing cross-profile mutation.
//
// Callers MUST NOT include profileId in the params map.
const verifyClaimCypher = `
MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
SET c.status                 = $status,
    c.entailment_verdict     = $entailmentVerdict,
    c.verifier_model         = $verifierModel,
    c.verified_at            = $verifiedAt,
    c.last_verifier_response = $lastVerifierResponse`

// verifyClaimRawBodyCypher persists only the raw verifier response body when
// the verifier call fails and the provider attached a parseable raw payload.
// The claim's status and verdict are intentionally left unchanged.
//
// Profile isolation: $profileId is injected by ScopedWrite.
const verifyClaimRawBodyCypher = `
MATCH (c:Claim {team_id: $profileId, claim_id: $claimId})
SET c.last_verifier_response = $lastVerifierResponse`

// verifyClaimServiceImpl implements VerifyClaimService.
type verifyClaimServiceImpl struct {
	reader        claimReader
	fragReader    supportedFragmentsReader
	writer        claimWriter
	verif         verifier.Verifier
	verifierModel string
	audit         AuditEmitter
	logger        *slog.Logger
	metrics       observability.DiscoverabilityMetrics
}

// Compile-time check that verifyClaimServiceImpl satisfies VerifyClaimService.
var _ VerifyClaimService = (*verifyClaimServiceImpl)(nil)

// NewVerifyClaimService constructs a ready-to-use VerifyClaimService.
//
// verifierModel is the human-readable model identifier stored on the claim
// after a successful verification pass (e.g. "gpt-4o-mini"). Pass an empty
// string if the model is unknown or not applicable.
//
// audit, logger, and metrics may be nil; audit failures are swallowed so the
// primary operation always succeeds. An absent logger emits no structured log
// lines; absent metrics are silently skipped.
func NewVerifyClaimService(
	reader claimReader,
	fragReader supportedFragmentsReader,
	writer claimWriter,
	verif verifier.Verifier,
	verifierModel string,
	audit AuditEmitter,
	logger *slog.Logger,
	metrics observability.DiscoverabilityMetrics,
) VerifyClaimService {
	return &verifyClaimServiceImpl{
		reader:        reader,
		fragReader:    fragReader,
		writer:        writer,
		verif:         verif,
		verifierModel: verifierModel,
		audit:         audit,
		logger:        logger,
		metrics:       metrics,
	}
}

// Verify runs entailment verification for claimID within profileID and returns
// the claim with its updated status and verdict fields populated.
//
// Algorithm:
//  1. Load claim by profileID/claimID → ErrClaimNotFound if absent.
//  2. Load supporting fragment contents via loadSupportingFragments.
//  3. Build a verifier.Request from the claim triple + fragment context.
//  4. Call verifier.Verify.
//  5. On success, map verdict to status/entailment_verdict:
//     entailed     → StatusValidated,  VerdictEntailed
//     contradicted → ClaimStatus("disputed"), VerdictContradicted
//     insufficient → status/verdict unchanged (claim stays candidate)
//     Persist verifier_model, verified_at, last_verifier_response.
//  6. On verifier failure, leave claim state unchanged; persist
//     last_verifier_response when the provider attached a raw body.
//  7. Emit claim.verify audit event (failure swallowed).
//  8. Emit verify_verdict_total metric.
func (s *verifyClaimServiceImpl) Verify(ctx context.Context, profileID string, claimID string) (*domain.Claim, error) {
	// Step 1: load the claim.
	//
	// Profile isolation: getClaimCypher includes {team_id: $profileId}
	// on the MATCH clause. A claim belonging to a different profile produces
	// zero rows, which is indistinguishable from "not found" — no existence leak.
	_, rows, err := s.reader.ScopedRead(ctx, profileID, getClaimCypher, map[string]any{
		"claimId": claimID,
	})
	if err != nil {
		return nil, fmt.Errorf("claim verify: load claim: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrClaimNotFound
	}
	claim := rowToClaim(profileID, rows[0])
	if err := ownership.RequireOwner(ctx, claim.OwnerProfileID); err != nil {
		return nil, err
	}

	// Step 2: load supporting fragments.
	//
	// Profile isolation enforced inside loadSupportingFragments: fragment IDs
	// that belong to a different profile are absent from the scoped query result
	// and cause ErrSupportingFragmentMissing to be returned.
	support, err := loadSupportingFragments(ctx, s.fragReader, profileID, claim.SupportedBy)
	if err != nil {
		return nil, fmt.Errorf("claim verify: load fragments: %w", err)
	}

	// Step 3: build the verifier request.
	//
	// Predicate is the natural-language form of the claim triple.
	// Context is the concatenated evidence text from supporting fragments.
	predicate := strings.Join([]string{claim.Subject, claim.Predicate, claim.Object}, " ")

	contextParts := make([]string, 0, len(support.Fragments))
	for _, frag := range support.Fragments {
		if frag.Content != "" {
			contextParts = append(contextParts, frag.Content)
		}
	}

	req := verifier.Request{
		ProfileID: profileID,
		Predicate: predicate,
		Context:   strings.Join(contextParts, "\n"),
		ValidFrom: claim.ValidFrom,
		ValidTo:   claim.ValidTo,
	}

	// Step 4: call the verifier.
	resp, verifyErr := s.verif.Verify(ctx, req)

	if verifyErr != nil {
		// Step 6: verifier failure — leave claim state unchanged.
		s.incVerifyMetric(ctx, "error")

		// Persist raw body when the provider attached one (diagnostic only;
		// write failure is non-fatal and only logged).
		if resp.RawJSON != "" {
			if _, writeErr := s.writer.ScopedWrite(ctx, profileID, verifyClaimRawBodyCypher, map[string]any{
				"claimId":              claimID,
				"lastVerifierResponse": resp.RawJSON,
			}); writeErr != nil && s.logger != nil {
				s.logger.Warn("claim verify: failed to persist raw verifier body",
					slog.String("team_id", profileID),
					slog.String("claim_id", claimID),
					slog.String("error", writeErr.Error()),
				)
			}
		}

		s.emitVerifyAudit(ctx, profileID, claimID, claim, "error")
		return nil, fmt.Errorf("claim verify: verifier call failed: %w", verifyErr)
	}

	// Step 5: map verdict → status and entailment_verdict.
	var (
		newStatus  domain.ClaimStatus
		newVerdict domain.EntailmentVerdict
		outcome    string // metric label
	)

	switch resp.Verdict {
	case "entailed":
		newStatus = domain.StatusValidated
		newVerdict = domain.VerdictEntailed
		outcome = "verified"
	case "contradicted":
		// "disputed" is the domain term for a claim contradicted by evidence.
		// domain.ClaimStatus does not yet export a StatusDisputed constant; the
		// string value is used directly, matching the spec and API documentation.
		newStatus = domain.ClaimStatus("disputed")
		newVerdict = domain.VerdictContradicted
		outcome = "refuted"
	case "insufficient":
		// Claim remains in its current state; only the audit fields are updated.
		newStatus = claim.Status
		newVerdict = domain.EntailmentVerdict("insufficient")
		outcome = "inconclusive"
	default:
		// The verifier interface guarantees exactly three valid verdict values.
		// A rogue implementation that violates this contract is treated as a
		// transient error rather than silently promoting/demoting the claim.
		s.incVerifyMetric(ctx, "error")
		s.emitVerifyAudit(ctx, profileID, claimID, claim, "error")
		return nil, fmt.Errorf("claim verify: unexpected verdict %q from verifier", resp.Verdict)
	}

	now := time.Now().UTC()

	// Persist the updated fields.
	summary, err := s.writer.ScopedWrite(ctx, profileID, verifyClaimCypher, map[string]any{
		"claimId":              claimID,
		"status":               string(newStatus),
		"entailmentVerdict":    string(newVerdict),
		"verifierModel":        s.verifierModel,
		"verifiedAt":           now,
		"lastVerifierResponse": resp.RawJSON,
	})
	if err != nil {
		s.incVerifyMetric(ctx, "error")
		return nil, fmt.Errorf("claim verify: persist: %w", err)
	}
	if !writeSummaryContainsUpdates(summary) {
		s.incVerifyMetric(ctx, "error")
		return nil, ErrClaimNotFound
	}

	// Step 8: emit metric for the successful verification path.
	s.incVerifyMetric(ctx, outcome)
	s.recordFunnelLatency(ctx, "claim_to_verify", claim.RecordedAt, now, outcome)

	// Mutate the in-memory claim so the caller receives the post-verify state.
	claim.Status = newStatus
	claim.EntailmentVerdict = newVerdict
	claim.VerifierModel = s.verifierModel
	claim.VerifiedAt = &now
	claim.LastVerifierResponse = resp.RawJSON

	// Step 7: emit audit event (failures swallowed).
	s.emitVerifyAudit(ctx, profileID, claimID, claim, resp.Verdict)

	return claim, nil
}

// incVerifyMetric bumps the verify_verdict_total counter when a metrics
// backend is wired. Nil-safe: if metrics is nil the call is a no-op.
func (s *verifyClaimServiceImpl) incVerifyMetric(ctx context.Context, outcome string) {
	observability.RecordVerifyVerdict(ctx, s.metrics, s.verifierModel, outcome)
}

func (s *verifyClaimServiceImpl) recordFunnelLatency(ctx context.Context, stage string, from, to time.Time, outcome string) {
	if from.IsZero() || to.IsZero() {
		return
	}
	observability.RecordMemoryFunnelLatency(ctx, s.metrics, stage, to.Sub(from).Seconds(), outcome)
}

func writeSummaryContainsUpdates(summary neo4j.ResultSummary) bool {
	return summary != nil && summary.Counters() != nil && summary.Counters().ContainsUpdates()
}

// emitVerifyAudit writes a claim.verify audit entry. Any error is logged and
// swallowed so audit failures never bubble up to the caller.
func (s *verifyClaimServiceImpl) emitVerifyAudit(
	ctx context.Context,
	profileID, claimID string,
	claim *domain.Claim,
	verdict string,
) {
	if s.audit == nil {
		return
	}

	entry := AuditLogEntry{
		ProfileID:  profileID,
		Timestamp:  time.Now().UTC(),
		Operation:  "claim.verify",
		EntityType: "claim",
		EntityID:   claimID,
		AfterPayload: map[string]any{
			"claim_id":           claimID,
			"team_id":            profileID,
			"status":             string(claim.Status),
			"entailment_verdict": string(claim.EntailmentVerdict),
			"verdict":            verdict,
		},
	}

	if auditErr := s.audit.Append(ctx, entry); auditErr != nil && s.logger != nil {
		s.logger.Warn("audit emit failed for claim.verify",
			slog.String("team_id", profileID),
			slog.String("claim_id", claimID),
			slog.String("error", auditErr.Error()),
		)
	}
}
