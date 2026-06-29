package claimservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/verifier"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Stub: verifier.Verifier
// ---------------------------------------------------------------------------

// stubVerifier implements verifier.Verifier for unit tests. It returns a
// preset Response/error pair so tests can drive each verdict branch without a
// live LLM.
type stubVerifier struct {
	resp    verifier.Response
	err     error
	lastReq verifier.Request
}

func (s *stubVerifier) Verify(_ context.Context, req verifier.Request) (verifier.Response, error) {
	s.lastReq = req
	return s.resp, s.err
}

var _ verifier.Verifier = (*stubVerifier)(nil)

// ---------------------------------------------------------------------------
// Stub: AuditEmitter
// ---------------------------------------------------------------------------

// capturingAudit records every emitted entry so tests can assert on audit
// output without a real database. Distinct from stubAudit in interfaces_test.go.
type capturingAudit struct {
	entries []AuditLogEntry
	err     error
}

func (a *capturingAudit) Append(_ context.Context, entry AuditLogEntry) error {
	if a.err != nil {
		return a.err
	}
	a.entries = append(a.entries, entry)
	return nil
}

var _ AuditEmitter = (*capturingAudit)(nil)

// ---------------------------------------------------------------------------
// Constants and helpers
// ---------------------------------------------------------------------------

const (
	verifyTestProfileA = "00000000-0000-0000-0000-000000000001"
	verifyTestProfileB = "00000000-0000-0000-0000-000000000002"
	verifyTestClaimID  = "claim-verify-abc-123"
)

// baseVerifyClaimRow returns a minimal Neo4j row representing a "candidate"
// claim with the given fragIDs in its supported_by collection.
func baseVerifyClaimRow(claimID, profileID string, fragIDs ...string) map[string]any {
	supportedBy := make([]any, len(fragIDs))
	for i, id := range fragIDs {
		supportedBy[i] = id
	}
	return map[string]any{
		"claim_id":                       claimID,
		"subject":                        "Alice",
		"predicate":                      "works at",
		"object":                         "Acme Corp",
		"modality":                       "assertion",
		"polarity":                       "positive",
		"status":                         "candidate",
		"entailment_verdict":             "insufficient",
		"extract_conf":                   0.9,
		"resolution_conf":                0.8,
		"source_quality":                 0.7,
		"recorded_at":                    time.Now().UTC(),
		"extraction_model":               "",
		"extraction_version":             "",
		"verifier_model":                 "",
		"pipeline_run_id":                "",
		"content_hash":                   "abc123",
		"idempotency_key":                "",
		"classification":                 map[string]any{},
		"classification_lattice_version": "v1",
		"supported_by":                   supportedBy,
	}
}

// buildVerifyService is a convenience constructor for the tests. It wires up
// two separate stubScopedReaders — one for claim rows and one for fragment rows
// — along with the rest of the dependencies.
func buildVerifyService(
	claimRows map[string][]map[string]any,
	fragRows map[string][]map[string]any,
	writer *stubClaimWriter,
	verif verifier.Verifier,
	verifierModel string,
	audit AuditEmitter,
	metrics observability.DiscoverabilityMetrics,
) VerifyClaimService {
	cr := &stubScopedReader{rowsByProfile: claimRows}
	fr := &stubScopedReader{rowsByProfile: fragRows}
	return NewVerifyClaimService(cr, fr, writer, verif, verifierModel, audit, nil, metrics)
}

// ---------------------------------------------------------------------------
// AC-27: verdict-to-status transitions
// ---------------------------------------------------------------------------

// TestVerifyClaimEntailed verifies that an "entailed" verdict transitions the
// claim to StatusValidated with VerdictEntailed and emits the "verified" metric.
func TestVerifyClaimEntailed(t *testing.T) {
	ctx := context.Background()

	claimRows := map[string][]map[string]any{
		verifyTestProfileA: {baseVerifyClaimRow(verifyTestClaimID, verifyTestProfileA, "frag-1")},
	}
	fragRows := map[string][]map[string]any{
		verifyTestProfileA: {
			{"fragment_id": "frag-1", "content": "Alice works at Acme Corp", "source_quality": 0.9, "classification": nil},
		},
	}
	writer := &stubClaimWriter{}
	verif := &stubVerifier{
		resp: verifier.Response{
			Verdict:    "entailed",
			Confidence: 0.95,
			Reasoning:  "clear match",
			RawJSON:    `{"verdict":"entailed"}`,
		},
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()

	svc := buildVerifyService(claimRows, fragRows, writer, verif, "gpt-4o-mini", nil, metrics)

	before := time.Now().UTC()
	got, err := svc.Verify(ctx, verifyTestProfileA, verifyTestClaimID)
	after := time.Now().UTC()

	require.NoError(t, err)
	require.NotNil(t, got)

	// Status / verdict transition.
	require.Equal(t, domain.StatusValidated, got.Status,
		"entailed verdict must transition claim to StatusValidated")
	require.Equal(t, domain.VerdictEntailed, got.EntailmentVerdict)

	// Verification metadata.
	require.Equal(t, "gpt-4o-mini", got.VerifierModel)
	require.NotNil(t, got.VerifiedAt)
	require.False(t, got.VerifiedAt.Before(before))
	require.False(t, got.VerifiedAt.After(after))
	require.Equal(t, `{"verdict":"entailed"}`, got.LastVerifierResponse)

	// Exactly one graph write.
	require.Len(t, writer.written, 1)

	// Metric: "verified".
	require.Equal(t, 1, metrics.VerifyVerdictCount("verified"))
	require.Equal(t, 0, metrics.VerifyVerdictCount("error"))
	funnel := metrics.MemoryFunnelSamples()
	require.Len(t, funnel, 1)
	require.Equal(t, "claim_to_verify", funnel[0].Stage)
	require.Equal(t, "verified", funnel[0].Outcome)
	require.GreaterOrEqual(t, funnel[0].Seconds, 0.0)
}

// TestVerifyClaimContradicted verifies that a "contradicted" verdict transitions
// the claim to the "disputed" status with VerdictContradicted.
func TestVerifyClaimContradicted(t *testing.T) {
	ctx := context.Background()

	claimRows := map[string][]map[string]any{
		verifyTestProfileA: {baseVerifyClaimRow(verifyTestClaimID, verifyTestProfileA, "frag-1")},
	}
	fragRows := map[string][]map[string]any{
		verifyTestProfileA: {
			{"fragment_id": "frag-1", "content": "Alice left Acme Corp years ago", "source_quality": 0.9, "classification": nil},
		},
	}
	writer := &stubClaimWriter{}
	verif := &stubVerifier{
		resp: verifier.Response{
			Verdict:    "contradicted",
			Confidence: 0.9,
			Reasoning:  "contradicted by evidence",
			RawJSON:    `{"verdict":"contradicted"}`,
		},
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()

	svc := buildVerifyService(claimRows, fragRows, writer, verif, "gpt-4o-mini", nil, metrics)

	got, err := svc.Verify(ctx, verifyTestProfileA, verifyTestClaimID)

	require.NoError(t, err)
	require.NotNil(t, got)

	// "disputed" is the domain term for contradicted claims.
	require.Equal(t, domain.ClaimStatus("disputed"), got.Status,
		"contradicted verdict must set status to 'disputed'")
	require.Equal(t, domain.VerdictContradicted, got.EntailmentVerdict)

	require.Len(t, writer.written, 1)
	require.Equal(t, 1, metrics.VerifyVerdictCount("refuted"))
}

// TestVerifyClaimInsufficient verifies that an "insufficient" verdict leaves
// the claim status unchanged (candidate) but still persists the audit fields.
func TestVerifyClaimInsufficient(t *testing.T) {
	ctx := context.Background()

	claimRows := map[string][]map[string]any{
		verifyTestProfileA: {baseVerifyClaimRow(verifyTestClaimID, verifyTestProfileA, "frag-1")},
	}
	fragRows := map[string][]map[string]any{
		verifyTestProfileA: {
			{"fragment_id": "frag-1", "content": "unrelated content", "source_quality": 0.5, "classification": nil},
		},
	}
	writer := &stubClaimWriter{}
	verif := &stubVerifier{
		resp: verifier.Response{
			Verdict:    "insufficient",
			Confidence: 0.3,
			Reasoning:  "not enough evidence",
			RawJSON:    `{"verdict":"insufficient"}`,
		},
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()

	svc := buildVerifyService(claimRows, fragRows, writer, verif, "gpt-4o-mini", nil, metrics)

	got, err := svc.Verify(ctx, verifyTestProfileA, verifyTestClaimID)

	require.NoError(t, err)
	require.NotNil(t, got)

	// Status must remain candidate; only the verdict/audit fields change.
	require.Equal(t, domain.StatusCandidate, got.Status,
		"insufficient verdict must leave claim status as candidate")
	require.Equal(t, domain.EntailmentVerdict("insufficient"), got.EntailmentVerdict)

	// Audit fields still populated for the insufficient path.
	require.NotNil(t, got.VerifiedAt)
	require.Equal(t, "gpt-4o-mini", got.VerifierModel)

	require.Len(t, writer.written, 1)
	require.Equal(t, 1, metrics.VerifyVerdictCount("inconclusive"))
}

func TestVerifyClaimPassesTemporalScopeToVerifier(t *testing.T) {
	ctx := context.Background()
	validFrom := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	validTo := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	row := baseVerifyClaimRow(verifyTestClaimID, verifyTestProfileA, "frag-1")
	row["valid_from"] = validFrom
	row["valid_to"] = validTo
	row["subject"] = "service TIER-W-001 owner"
	row["predicate"] = "uses"
	row["object"] = "owner-coral"

	claimRows := map[string][]map[string]any{
		verifyTestProfileA: {row},
	}
	fragRows := map[string][]map[string]any{
		verifyTestProfileA: {
			{"fragment_id": "frag-1", "content": "Since 2026-06-20, service TIER-W-001 owner uses owner-coral.", "source_quality": 0.9, "classification": nil},
		},
	}
	writer := &stubClaimWriter{}
	verif := &stubVerifier{
		resp: verifier.Response{
			Verdict:    "entailed",
			Confidence: 0.95,
			Reasoning:  "temporal claim supported",
			RawJSON:    `{"verdict":"entailed"}`,
		},
	}
	svc := buildVerifyService(claimRows, fragRows, writer, verif, "verifier-model", nil, nil)

	got, err := svc.Verify(ctx, verifyTestProfileA, verifyTestClaimID)

	require.NoError(t, err)
	require.Equal(t, domain.StatusValidated, got.Status)
	require.NotNil(t, verif.lastReq.ValidFrom)
	require.NotNil(t, verif.lastReq.ValidTo)
	require.Equal(t, validFrom, *verif.lastReq.ValidFrom)
	require.Equal(t, validTo, *verif.lastReq.ValidTo)
	require.Equal(t, "service TIER-W-001 owner uses owner-coral", verif.lastReq.Predicate)
}

// ---------------------------------------------------------------------------
// AC-28: error and not-found paths
// ---------------------------------------------------------------------------

// TestVerifyClaimNotFound verifies that ErrClaimNotFound is returned when the
// claim does not exist for the given profile, and that no write occurs.
func TestVerifyClaimNotFound(t *testing.T) {
	ctx := context.Background()

	claimRows := map[string][]map[string]any{
		verifyTestProfileA: {}, // empty → no matching claim
	}
	fragRows := map[string][]map[string]any{}
	writer := &stubClaimWriter{}

	svc := buildVerifyService(claimRows, fragRows, writer, &stubVerifier{}, "", nil, nil)

	_, err := svc.Verify(ctx, verifyTestProfileA, verifyTestClaimID)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrClaimNotFound)
	require.Empty(t, writer.written, "no write must occur when claim is missing")
}

func TestVerifyClaimReturnsNotFoundWhenPersistMatchesNoRows(t *testing.T) {
	ctx := context.Background()

	claimRows := map[string][]map[string]any{
		verifyTestProfileA: {baseVerifyClaimRow(verifyTestClaimID, verifyTestProfileA)},
	}
	fragRows := map[string][]map[string]any{
		verifyTestProfileA: {},
	}
	writer := &stubClaimWriter{
		summary: stubResultSummary{counters: stubCounters{}},
	}
	verif := &stubVerifier{
		resp: verifier.Response{
			Verdict:    "entailed",
			Confidence: 0.95,
			Reasoning:  "clear match",
			RawJSON:    `{"verdict":"entailed"}`,
		},
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	audit := &capturingAudit{}

	svc := buildVerifyService(claimRows, fragRows, writer, verif, "gpt-4o-mini", audit, metrics)

	_, err := svc.Verify(ctx, verifyTestProfileA, verifyTestClaimID)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrClaimNotFound)
	require.Len(t, writer.written, 1, "the persist attempt still happens after the initial read")
	require.Equal(t, 1, metrics.VerifyVerdictCount("error"))
	require.Equal(t, 0, metrics.VerifyVerdictCount("verified"))
	require.Empty(t, audit.entries, "successful verification audit must not emit when persistence matched no rows")
}

// TestVerifyClaimVerifierError verifies that a verifier failure propagates as
// an error, leaves the claim unchanged, and emits the "error" metric.
func TestVerifyClaimVerifierError(t *testing.T) {
	ctx := context.Background()

	claimRows := map[string][]map[string]any{
		verifyTestProfileA: {baseVerifyClaimRow(verifyTestClaimID, verifyTestProfileA)},
	}
	fragRows := map[string][]map[string]any{
		verifyTestProfileA: {},
	}
	writer := &stubClaimWriter{}
	verif := &stubVerifier{err: errors.New("provider unreachable")}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	audit := &capturingAudit{}

	svc := buildVerifyService(claimRows, fragRows, writer, verif, "", audit, metrics)

	_, err := svc.Verify(ctx, verifyTestProfileA, verifyTestClaimID)

	require.Error(t, err)
	require.Contains(t, err.Error(), "verifier call failed",
		"error must describe what went wrong")

	// Claim state unchanged — no write to status/verdict.
	require.Empty(t, writer.written, "claim state must not be written on verifier failure")

	// Metric and audit must still fire.
	require.Equal(t, 1, metrics.VerifyVerdictCount("error"))
	require.Len(t, audit.entries, 1)
	require.Equal(t, "claim.verify", audit.entries[0].Operation)
}

// TestVerifyClaimAuditEmitted verifies that claim.verify audit events are
// emitted with the correct fields after a successful verification.
func TestVerifyClaimAuditEmitted(t *testing.T) {
	ctx := context.Background()

	claimRows := map[string][]map[string]any{
		verifyTestProfileA: {baseVerifyClaimRow(verifyTestClaimID, verifyTestProfileA)},
	}
	fragRows := map[string][]map[string]any{
		verifyTestProfileA: {},
	}
	writer := &stubClaimWriter{}
	verif := &stubVerifier{
		resp: verifier.Response{
			Verdict:    "entailed",
			Confidence: 0.99,
			Reasoning:  "confirmed",
			RawJSON:    "{}",
		},
	}
	audit := &capturingAudit{}

	svc := buildVerifyService(claimRows, fragRows, writer, verif, "model-x", audit, nil)

	_, err := svc.Verify(ctx, verifyTestProfileA, verifyTestClaimID)

	require.NoError(t, err)
	require.Len(t, audit.entries, 1, "exactly one audit entry expected")

	entry := audit.entries[0]
	require.Equal(t, "claim.verify", entry.Operation)
	require.Equal(t, verifyTestProfileA, entry.ProfileID)
	require.Equal(t, verifyTestClaimID, entry.EntityID)
	require.Equal(t, "claim", entry.EntityType)
	require.Equal(t, verifyTestClaimID, entry.AfterPayload["claim_id"])
	require.Equal(t, "entailed", entry.AfterPayload["verdict"])
	require.Equal(t, string(domain.StatusValidated), entry.AfterPayload["status"])
}

// ---------------------------------------------------------------------------
// AC-30: profile isolation (mandatory per .claude/rules/profile-isolation.md)
// ---------------------------------------------------------------------------

// TestVerifyClaim_CrossProfileIsolation verifies that a claim belonging to
// profile A cannot be read or mutated when the caller supplies profile B.
//
// Security invariant: ScopedRead filters by $profileId on the Claim node. A
// claim owned by a different profile produces zero rows, which is
// indistinguishable from "not found" — no cross-profile existence leakage.
func TestVerifyClaim_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()

	// Profile A has a claim; profile B has none.
	claimRows := map[string][]map[string]any{
		verifyTestProfileA: {baseVerifyClaimRow(verifyTestClaimID, verifyTestProfileA)},
		verifyTestProfileB: {}, // profile B sees no rows
	}
	fragRows := map[string][]map[string]any{
		verifyTestProfileA: {},
		verifyTestProfileB: {},
	}
	writer := &stubClaimWriter{}
	verif := &stubVerifier{
		resp: verifier.Response{Verdict: "entailed", Confidence: 0.9, Reasoning: "ok", RawJSON: "{}"},
	}

	svc := buildVerifyService(claimRows, fragRows, writer, verif, "", nil, nil)

	// Profile B must not be able to verify profile A's claim.
	_, err := svc.Verify(ctx, verifyTestProfileB, verifyTestClaimID)
	require.Error(t, err, "profile B must not verify profile A's claim")
	require.ErrorIs(t, err, ErrClaimNotFound,
		"cross-profile access must return ErrClaimNotFound — no existence leak")
	require.Empty(t, writer.written, "no write must occur on cross-profile miss")

	// Profile A can verify its own claim.
	got, errA := svc.Verify(ctx, verifyTestProfileA, verifyTestClaimID)
	require.NoError(t, errA, "profile A must be able to verify its own claim")
	require.NotNil(t, got)
	require.Equal(t, verifyTestProfileA, got.ProfileID,
		"returned claim must carry profile A's identity")

	// The result for profile A must not contain anything from profile B.
	bResults := []string{got.ClaimID}
	require.NotContains(t, bResults, "claim-owned-by-profile-b",
		"profile B's claim IDs must not appear in profile A's result")
}

// TestVerifyClaimFragmentIsolation verifies that supporting fragments belonging
// to profile A cannot be loaded when verifying a claim as profile B.
func TestVerifyClaimFragmentIsolation(t *testing.T) {
	ctx := context.Background()

	// Profile B has a claim that references a fragment ID that only profile A
	// owns. The stub models what Neo4j enforces in production: profile B's
	// scoped query for "frag-a1" returns zero rows.
	profileBClaim := baseVerifyClaimRow(verifyTestClaimID, verifyTestProfileB, "frag-a1")

	claimRows := map[string][]map[string]any{
		verifyTestProfileB: {profileBClaim},
	}
	fragRows := map[string][]map[string]any{
		verifyTestProfileA: {
			{"fragment_id": "frag-a1", "content": "profile A private data", "source_quality": 0.9, "classification": nil},
		},
		verifyTestProfileB: {}, // profile B cannot see frag-a1
	}
	writer := &stubClaimWriter{}
	verif := &stubVerifier{
		resp: verifier.Response{Verdict: "entailed", Confidence: 0.9, Reasoning: "ok", RawJSON: "{}"},
	}

	svc := buildVerifyService(claimRows, fragRows, writer, verif, "", nil, nil)

	_, err := svc.Verify(ctx, verifyTestProfileB, verifyTestClaimID)
	require.Error(t, err, "cross-profile fragment reference must be rejected")
	require.Empty(t, writer.written, "no write must occur when fragment isolation fails")
}
