package claimservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

// stubClaimReader implements claimReader for unit tests.
//
// Rows are keyed by profileID to model Neo4j's profile-scoped isolation:
// ScopedRead returns only the rows registered for the given profile, mirroring
// the {team_id: $profileId} filter in getClaimCypher.
type stubClaimReader struct {
	rowsByProfile map[string][]map[string]any
	err           error
}

func (s *stubClaimReader) ScopedRead(
	_ context.Context,
	profileID string,
	_ string,
	_ map[string]any,
) (neo4j.ResultSummary, []map[string]any, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	return nil, s.rowsByProfile[profileID], nil
}

// Compile-time check: stubClaimReader satisfies the package-internal interface.
var _ claimReader = (*stubClaimReader)(nil)

// TestGetClaim covers AC-15: claim read must enforce same-profile 404 and
// hydrate supported_by from outgoing SUPPORTED_BY edges (Claim→SourceFragment).
func TestGetClaim(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	const claimID = "claim-abc"

	t.Run("returns claim when profile and ID match", func(t *testing.T) {
		now := time.Now().UTC()
		row := map[string]any{
			"claim_id":                       claimID,
			"subject":                        "Alice",
			"predicate":                      "knows",
			"object":                         "Bob",
			"modality":                       "assertion",
			"polarity":                       "+",
			"speaker":                        "narrator",
			"span_start":                     int64(5),
			"span_end":                       int64(20),
			"valid_from":                     nil,
			"valid_to":                       nil,
			"recorded_at":                    now,
			"recorded_to":                    nil,
			"extract_conf":                   0.92,
			"resolution_conf":                0.80,
			"source_quality":                 0.75,
			"entailment_verdict":             "insufficient",
			"status":                         "candidate",
			"last_verifier_response":         "",
			"verified_at":                    nil,
			"extraction_model":               "gpt-4",
			"extraction_version":             "1.0",
			"verifier_model":                 "",
			"pipeline_run_id":                "run-42",
			"content_hash":                   "deadbeef",
			"idempotency_key":                "idem-1",
			"classification":                 map[string]any{"confidentiality": "internal"},
			"classification_lattice_version": "v1",
			"supported_by":                   []any{"frag-1", "frag-2"},
		}

		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {row},
			},
		}
		svc := NewGetClaimService(reader, nil)

		got, err := svc.Get(ctx, profileID, claimID)

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, claimID, got.ClaimID)
		require.Equal(t, profileID, got.ProfileID)
		require.Equal(t, "Alice", got.Subject)
		require.Equal(t, "knows", got.Predicate)
		require.Equal(t, "Bob", got.Object)
		require.Equal(t, domain.ModalityAssertion, got.Modality)
		require.Equal(t, domain.PolarityPositive, got.Polarity)
		require.Equal(t, "narrator", got.Speaker)
		require.Equal(t, 5, got.SpanStart)
		require.Equal(t, 20, got.SpanEnd)
		require.InDelta(t, 0.92, got.ExtractConf, 1e-9)
		require.InDelta(t, 0.80, got.ResolutionConf, 1e-9)
		require.InDelta(t, 0.75, got.SourceQuality, 1e-9)
		require.Equal(t, domain.VerdictUnverified, got.EntailmentVerdict)
		require.Equal(t, domain.StatusCandidate, got.Status)
		require.Equal(t, "gpt-4", got.ExtractionModel)
		require.Equal(t, "1.0", got.ExtractionVersion)
		require.Equal(t, "run-42", got.PipelineRunID)
		require.Equal(t, "deadbeef", got.ContentHash)
		require.Equal(t, "idem-1", got.IdempotencyKey)
		require.Equal(t, "v1", got.ClassificationLatticeVersion)
		require.Equal(t, "internal", got.Classification["confidentiality"])
		require.ElementsMatch(t, []string{"frag-1", "frag-2"}, got.SupportedBy)
	})

	t.Run("hydrates supported_by from outgoing Claim→SourceFragment SUPPORTED_BY edge collect", func(t *testing.T) {
		row := map[string]any{
			"claim_id":                       claimID,
			"subject":                        "Sun",
			"predicate":                      "is",
			"object":                         "star",
			"modality":                       "assertion",
			"polarity":                       "+",
			"speaker":                        "",
			"span_start":                     int64(0),
			"span_end":                       int64(0),
			"valid_from":                     nil,
			"valid_to":                       nil,
			"recorded_at":                    time.Now().UTC(),
			"recorded_to":                    nil,
			"extract_conf":                   0.0,
			"resolution_conf":                0.0,
			"source_quality":                 0.0,
			"entailment_verdict":             "insufficient",
			"status":                         "candidate",
			"last_verifier_response":         "",
			"verified_at":                    nil,
			"extraction_model":               "",
			"extraction_version":             "",
			"verifier_model":                 "",
			"pipeline_run_id":                "",
			"content_hash":                   "hash1",
			"idempotency_key":                "",
			"classification":                 nil,
			"classification_lattice_version": "",
			// collect(sf.fragment_id) from two outgoing Claim→SourceFragment SUPPORTED_BY edges
			"supported_by": []any{"frag-x", "frag-y"},
		}

		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {row},
			},
		}
		svc := NewGetClaimService(reader, nil)

		got, err := svc.Get(ctx, profileID, claimID)

		require.NoError(t, err)
		require.NotNil(t, got)
		require.ElementsMatch(t, []string{"frag-x", "frag-y"}, got.SupportedBy,
			"supported_by must be hydrated from outgoing Claim→SourceFragment SUPPORTED_BY edge collect")
	})

	t.Run("decodes JSON encoded classification", func(t *testing.T) {
		row := map[string]any{
			"claim_id":                       claimID,
			"subject":                        "Alice",
			"predicate":                      "works_at",
			"object":                         "Acme",
			"modality":                       "assertion",
			"polarity":                       "+",
			"speaker":                        "",
			"span_start":                     int64(0),
			"span_end":                       int64(0),
			"valid_from":                     nil,
			"valid_to":                       nil,
			"recorded_at":                    time.Now().UTC(),
			"recorded_to":                    nil,
			"extract_conf":                   0.0,
			"resolution_conf":                0.0,
			"source_quality":                 0.0,
			"entailment_verdict":             "insufficient",
			"status":                         "candidate",
			"last_verifier_response":         "",
			"verified_at":                    nil,
			"extraction_model":               "",
			"extraction_version":             "",
			"verifier_model":                 "",
			"pipeline_run_id":                "",
			"content_hash":                   "hash-json",
			"idempotency_key":                "",
			"classification":                 nil,
			"classification_json":            `{"confidentiality":"internal","pii":"sensitive"}`,
			"classification_lattice_version": "v1",
			"supported_by":                   []any{},
		}

		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {row},
			},
		}
		svc := NewGetClaimService(reader, nil)

		got, err := svc.Get(ctx, profileID, claimID)

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "internal", got.Classification["confidentiality"])
		require.Equal(t, "sensitive", got.Classification["pii"])
	})

	t.Run("returns empty supported_by when no outgoing Claim→SourceFragment SUPPORTED_BY edges exist", func(t *testing.T) {
		row := map[string]any{
			"claim_id":                       claimID,
			"subject":                        "Moon",
			"predicate":                      "orbits",
			"object":                         "Earth",
			"modality":                       "assertion",
			"polarity":                       "+",
			"speaker":                        "",
			"span_start":                     int64(0),
			"span_end":                       int64(0),
			"valid_from":                     nil,
			"valid_to":                       nil,
			"recorded_at":                    time.Now().UTC(),
			"recorded_to":                    nil,
			"extract_conf":                   0.0,
			"resolution_conf":                0.0,
			"source_quality":                 0.0,
			"entailment_verdict":             "insufficient",
			"status":                         "candidate",
			"last_verifier_response":         "",
			"verified_at":                    nil,
			"extraction_model":               "",
			"extraction_version":             "",
			"verifier_model":                 "",
			"pipeline_run_id":                "",
			"content_hash":                   "hash2",
			"idempotency_key":                "",
			"classification":                 nil,
			"classification_lattice_version": "",
			// OPTIONAL MATCH (c)-[:SUPPORTED_BY]->(sf) found no fragments — collect returns []
			"supported_by": []any{},
		}

		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {row},
			},
		}
		svc := NewGetClaimService(reader, nil)

		got, err := svc.Get(ctx, profileID, claimID)

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Empty(t, got.SupportedBy, "supported_by must be empty when no edges exist")
	})

	t.Run("returns ErrClaimNotFound when claim does not exist", func(t *testing.T) {
		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {}, // no rows — MATCH found nothing
			},
		}
		svc := NewGetClaimService(reader, nil)

		got, err := svc.Get(ctx, profileID, "nonexistent-claim")

		require.Nil(t, got)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrClaimNotFound),
			"missing claim must return ErrClaimNotFound, got: %v", err)
	})

	t.Run("propagates reader error", func(t *testing.T) {
		readerErr := errors.New("neo4j unavailable")
		reader := &stubClaimReader{err: readerErr}
		svc := NewGetClaimService(reader, nil)

		got, err := svc.Get(ctx, profileID, claimID)

		require.Nil(t, got)
		require.Error(t, err)
		require.Contains(t, err.Error(), "neo4j unavailable")
	})

	t.Run("temporal fields are propagated correctly", func(t *testing.T) {
		validFrom := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		validTo := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		recordedAt := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
		verifiedAt := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)

		row := map[string]any{
			"claim_id":                       claimID,
			"subject":                        "X",
			"predicate":                      "y",
			"object":                         "Z",
			"modality":                       "assertion",
			"polarity":                       "+",
			"speaker":                        "",
			"span_start":                     int64(0),
			"span_end":                       int64(0),
			"valid_from":                     validFrom,
			"valid_to":                       validTo,
			"recorded_at":                    recordedAt,
			"recorded_to":                    nil,
			"extract_conf":                   0.0,
			"resolution_conf":                0.0,
			"source_quality":                 0.0,
			"entailment_verdict":             "entailed",
			"status":                         "validated",
			"last_verifier_response":         "supported by evidence",
			"verified_at":                    verifiedAt,
			"extraction_model":               "",
			"extraction_version":             "",
			"verifier_model":                 "claude-3",
			"pipeline_run_id":                "",
			"content_hash":                   "hash3",
			"idempotency_key":                "",
			"classification":                 nil,
			"classification_lattice_version": "",
			"supported_by":                   []any{},
		}

		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {row},
			},
		}
		svc := NewGetClaimService(reader, nil)

		got, err := svc.Get(ctx, profileID, claimID)

		require.NoError(t, err)
		require.NotNil(t, got)
		require.NotNil(t, got.ValidFrom)
		require.Equal(t, validFrom, *got.ValidFrom)
		require.NotNil(t, got.ValidTo)
		require.Equal(t, validTo, *got.ValidTo)
		require.Equal(t, recordedAt, got.RecordedAt)
		require.NotNil(t, got.VerifiedAt)
		require.Equal(t, verifiedAt, *got.VerifiedAt)
		require.Equal(t, "supported by evidence", got.LastVerifierResponse)
		require.Equal(t, "claude-3", got.VerifierModel)
		require.Equal(t, domain.VerdictEntailed, got.EntailmentVerdict)
		require.Equal(t, domain.StatusValidated, got.Status)
	})
}

func TestRowToClaimForExternalUse_HydratesOwnerFallbackAndEvidence(t *testing.T) {
	now := time.Now().UTC()
	verifiedAt := now.Add(time.Minute)
	recordedTo := now.Add(time.Hour)
	row := map[string]any{
		"claim_id":                       "claim-export",
		"created_by_profile_id":          "creator-profile",
		"created_by_profile_name":        "Creator",
		"subject":                        "Alice",
		"predicate":                      "prefers",
		"object":                         "TypeScript",
		"modality":                       string(domain.ModalityAssertion),
		"polarity":                       string(domain.PolarityPositive),
		"speaker":                        "Alice",
		"span_start":                     3,
		"span_end":                       11,
		"recorded_at":                    now,
		"recorded_to":                    recordedTo,
		"extract_conf":                   0.83,
		"resolution_conf":                0.74,
		"source_quality":                 0.91,
		"entailment_verdict":             string(domain.VerdictEntailed),
		"status":                         string(domain.StatusValidated),
		"last_verifier_response":         "entailed",
		"verified_at":                    verifiedAt,
		"extraction_model":               "extractor",
		"extraction_version":             "v2",
		"verifier_model":                 "verifier",
		"pipeline_run_id":                "run-export",
		"content_hash":                   "hash-export",
		"idempotency_key":                "idem-export",
		"classification_json":            `{"platform":"web"}`,
		"classification_lattice_version": "v3",
		"supported_by":                   []any{"frag-1", "", 42, "frag-2"},
		"evidence": []any{
			map[string]any{
				"fragment_id":        "frag-1",
				"speaker":            "Alice",
				"span_start":         int64(3),
				"span_end":           11,
				"extract_conf":       float32(0.66),
				"extraction_model":   "extractor",
				"extraction_version": "v2",
				"pipeline_run_id":    "run-export",
				"authority":          string(domain.AuthorityPrimary),
			},
			map[string]any{
				"fragment_id":  "frag-default",
				"span_start":   "bad",
				"span_end":     "bad",
				"extract_conf": "bad",
			},
			map[string]any{"fragment_id": ""},
			"not evidence",
		},
	}

	got := RowToClaimForExternalUse("team-profile", row)

	require.Equal(t, "claim-export", got.ClaimID)
	require.Equal(t, "team-profile", got.ProfileID)
	require.Equal(t, "creator-profile", got.OwnerProfileID)
	require.Equal(t, "Creator", got.OwnerProfileName)
	require.Equal(t, 3, got.SpanStart)
	require.Equal(t, 11, got.SpanEnd)
	require.Equal(t, &recordedTo, got.RecordedTo)
	require.Equal(t, &verifiedAt, got.VerifiedAt)
	require.Equal(t, "web", got.Classification["platform"])
	require.Equal(t, []string{"frag-1", "frag-2"}, got.SupportedBy)
	require.Len(t, got.Evidence, 2)
	require.Equal(t, "frag-1", got.Evidence[0].FragmentID)
	require.Equal(t, 3, got.Evidence[0].SpanStart)
	require.Equal(t, 11, got.Evidence[0].SpanEnd)
	require.InDelta(t, 0.66, got.Evidence[0].ExtractConf, 1e-6)
	require.Equal(t, domain.AuthorityPrimary, got.Evidence[0].Authority)
	require.Equal(t, "frag-default", got.Evidence[1].FragmentID)
	require.Zero(t, got.Evidence[1].SpanStart)
	require.Zero(t, got.Evidence[1].SpanEnd)
	require.Zero(t, got.Evidence[1].ExtractConf)
}

// TestGetClaim_CrossProfileIsolation verifies that a claim belonging to profile
// A is not returned when querying as profile B, and that existence under the
// other profile is not leaked. This is a mandatory security test per
// .claude/rules/profile-isolation.md.
func TestGetClaim_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()
	const profileA = "00000000-0000-0000-0000-000000000001"
	const profileB = "00000000-0000-0000-0000-000000000002"
	const sharedClaimID = "claim-shared-id"

	// The stub models Neo4j's profile-scoped isolation: ScopedRead returns only
	// the rows registered for the given profileID, mirroring the
	// {team_id: $profileId} MATCH filter. Profile B gets no rows even though
	// the claim ID matches — exactly as production Neo4j would behave.
	row := map[string]any{
		"claim_id":                       sharedClaimID,
		"subject":                        "Alice",
		"predicate":                      "knows",
		"object":                         "Bob",
		"modality":                       "assertion",
		"polarity":                       "+",
		"speaker":                        "",
		"span_start":                     int64(0),
		"span_end":                       int64(0),
		"valid_from":                     nil,
		"valid_to":                       nil,
		"recorded_at":                    time.Now().UTC(),
		"recorded_to":                    nil,
		"extract_conf":                   0.0,
		"resolution_conf":                0.0,
		"source_quality":                 0.0,
		"entailment_verdict":             "insufficient",
		"status":                         "candidate",
		"last_verifier_response":         "",
		"verified_at":                    nil,
		"extraction_model":               "",
		"extraction_version":             "",
		"verifier_model":                 "",
		"pipeline_run_id":                "",
		"content_hash":                   "hash-a",
		"idempotency_key":                "",
		"classification":                 nil,
		"classification_lattice_version": "",
		"supported_by":                   []any{},
	}

	reader := &stubClaimReader{
		rowsByProfile: map[string][]map[string]any{
			profileA: {row}, // claim belongs to profile A
			profileB: {},    // profile B sees nothing for this claim ID
		},
	}
	svc := NewGetClaimService(reader, nil)

	// Profile B must not receive profile A's claim.
	gotB, errB := svc.Get(ctx, profileB, sharedClaimID)
	require.Nil(t, gotB, "profile B must not receive profile A's claim")
	require.Error(t, errB)
	require.True(t, errors.Is(errB, ErrClaimNotFound),
		"profile B query must return ErrClaimNotFound (same error as truly absent claim — no existence leak), got: %v", errB)

	// Profile A can retrieve its own claim without error.
	gotA, errA := svc.Get(ctx, profileA, sharedClaimID)
	require.NoError(t, errA, "profile A must be able to retrieve its own claim")
	require.NotNil(t, gotA)
	require.Equal(t, profileA, gotA.ProfileID,
		"returned claim must carry profile A's ID")
	require.Equal(t, sharedClaimID, gotA.ClaimID)

	// The profile A result must not contain profile B's ID.
	bResults := []string{gotA.ProfileID}
	require.NotContains(t, bResults, profileB,
		"profile A's claim must not reference profile B")
}

func TestGetClaimByIDsAndEvidenceMapping(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	now := time.Now().UTC()

	row := map[string]any{
		"claim_id":                       "claim-1",
		"created_by_profile_id":          "creator-1",
		"created_by_profile_name":        "Creator",
		"subject":                        "Alice",
		"predicate":                      "likes",
		"object":                         "Go",
		"modality":                       "assertion",
		"polarity":                       "+",
		"speaker":                        "Alice",
		"span_start":                     int(3),
		"span_end":                       int64(9),
		"recorded_at":                    now,
		"extract_conf":                   0.8,
		"resolution_conf":                0.7,
		"source_quality":                 0.6,
		"entailment_verdict":             "entailed",
		"status":                         "validated",
		"supported_by":                   []any{"fragment-1", "", 12},
		"classification":                 map[string]any{"topic": "prefs"},
		"classification_lattice_version": "v1",
		"evidence": []any{
			map[string]any{
				"fragment_id":        "fragment-1",
				"speaker":            "Alice",
				"span_start":         int64(3),
				"span_end":           int(9),
				"extract_conf":       float32(0.8),
				"extraction_model":   "model-a",
				"extraction_version": "1",
				"pipeline_run_id":    "run-1",
				"authority":          "primary",
			},
			map[string]any{"fragment_id": ""},
			"not-a-map",
		},
	}
	reader := &stubClaimReader{
		rowsByProfile: map[string][]map[string]any{
			profileID: {row, {"claim_id": "", "recorded_at": now}},
		},
	}
	svc := NewGetClaimService(reader, nil)
	batchSvc := svc.(interface {
		GetByIDs(ctx context.Context, profileID string, claimIDs []string) (map[string]*domain.Claim, error)
	})

	empty, err := batchSvc.GetByIDs(ctx, profileID, nil)
	require.NoError(t, err)
	require.Empty(t, empty)

	got, err := batchSvc.GetByIDs(ctx, profileID, []string{"claim-1", "missing"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	claim := got["claim-1"]
	require.NotNil(t, claim)
	require.Equal(t, []string{"fragment-1"}, claim.SupportedBy)
	require.Len(t, claim.Evidence, 1)
	require.Equal(t, "fragment-1", claim.Evidence[0].FragmentID)
	require.Equal(t, 3, claim.Evidence[0].SpanStart)
	require.Equal(t, 9, claim.Evidence[0].SpanEnd)
	require.InDelta(t, 0.8, claim.Evidence[0].ExtractConf, 1e-6)
	require.Equal(t, domain.AuthorityPrimary, claim.Evidence[0].Authority)

	reader.err = errors.New("read failed")
	got, err = batchSvc.GetByIDs(ctx, profileID, []string{"claim-1"})
	require.Nil(t, got)
	require.ErrorContains(t, err, "claim batch get")
}
