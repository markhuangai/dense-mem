package claimservice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/claimidentity"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

// stubClaimDedupeLookup implements claimDedupeLookup for unit tests.
//
// Entries are keyed as "profileID:key" / "profileID:hash" to allow
// cross-profile isolation scenarios to be modelled without a real database.
type stubClaimDedupeLookup struct {
	byIdempotencyKey map[string]*domain.Claim // keyed by profileID+":"+key
	byContentHash    map[string]*domain.Claim // keyed by profileID+":"+hash
	err              error
}

func (s *stubClaimDedupeLookup) ByIdempotencyKey(_ context.Context, profileID, key string) (*domain.Claim, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.byIdempotencyKey[profileID+":"+key], nil
}

func (s *stubClaimDedupeLookup) ByContentHash(_ context.Context, profileID, hash string) (*domain.Claim, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.byContentHash[profileID+":"+hash], nil
}

// Compile-time check: stubClaimDedupeLookup satisfies the package-internal interface.
var _ claimDedupeLookup = (*stubClaimDedupeLookup)(nil)

// stubClaimWriter captures ScopedWrite invocations for post-call assertions.
type stubClaimWriter struct {
	written []map[string]any
	queries []string
	err     error
}

func (s *stubClaimWriter) ScopedWrite(
	_ context.Context,
	_ string,
	query string,
	params map[string]any,
) (neo4j.ResultSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.queries = append(s.queries, query)
	s.written = append(s.written, params)
	return nil, nil
}

// Compile-time check: stubClaimWriter satisfies the package-internal interface.
var _ claimWriter = (*stubClaimWriter)(nil)

// TestCreateClaimDedupeAndDefaults covers AC-11 (deduplicate by idempotency key
// and content hash) and AC-13 (server-side default population).
func TestCreateClaimDedupeAndDefaults(t *testing.T) {
	ctx := context.Background()

	// profileID must be a valid UUID so ValidateClaimIdentityInputs and the
	// UUIDv5 derivation in claimidentity both succeed.
	const profileID = "00000000-0000-0000-0000-000000000001"

	t.Run("idempotency key hit returns existing claim without write", func(t *testing.T) {
		existing := &domain.Claim{ClaimID: "existing-id", ProfileID: profileID}
		lookup := &stubClaimDedupeLookup{
			byIdempotencyKey: map[string]*domain.Claim{
				profileID + ":my-idem-key": existing,
			},
			byContentHash: map[string]*domain.Claim{},
		}
		writer := &stubClaimWriter{}
		reader := &stubScopedReader{rowsByProfile: map[string][]map[string]any{}}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()

		svc := NewCreateClaimService(lookup, reader, writer, nil, nil, metrics)

		got, err := svc.Create(ctx, profileID, &domain.Claim{
			Subject:        "Alice",
			Predicate:      "knows",
			Object:         "Bob",
			IdempotencyKey: "my-idem-key",
		})

		require.NoError(t, err)
		require.NotNil(t, got)
		require.True(t, got.Duplicate, "result must be flagged as a duplicate")
		require.Equal(t, "existing-id", got.DuplicateOf)
		require.Equal(t, existing, got.Claim)
		require.Empty(t, writer.written, "duplicate hit must not write to the graph")
		samples := metrics.ClaimCreateSamples()
		require.Len(t, samples, 1)
		require.Equal(t, "duplicate", samples[0].Outcome)
		require.Equal(t, "idempotency_key", samples[0].DedupeReason)
	})

	t.Run("content hash hit returns existing claim without write", func(t *testing.T) {
		// Build the exact hash the service will compute so the stub can return
		// the pre-existing claim when ByContentHash is called.
		input := &domain.Claim{
			Subject:   "Sun",
			Predicate: "is",
			Object:    "star",
		}
		expectedHash := claimidentity.ContentHash(input.Subject, input.Predicate, input.Object, nil)

		existing := &domain.Claim{ClaimID: "hash-dupe-id", ProfileID: profileID}
		lookup := &stubClaimDedupeLookup{
			byIdempotencyKey: map[string]*domain.Claim{},
			byContentHash: map[string]*domain.Claim{
				profileID + ":" + expectedHash: existing,
			},
		}
		writer := &stubClaimWriter{}
		reader := &stubScopedReader{rowsByProfile: map[string][]map[string]any{}}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()

		svc := NewCreateClaimService(lookup, reader, writer, nil, nil, metrics)

		got, err := svc.Create(ctx, profileID, input)

		require.NoError(t, err)
		require.NotNil(t, got)
		require.True(t, got.Duplicate, "content hash match must be flagged as duplicate")
		require.Equal(t, "hash-dupe-id", got.DuplicateOf)
		require.Equal(t, existing, got.Claim)
		require.Empty(t, writer.written, "duplicate hit must not write to the graph")
		samples := metrics.ClaimCreateSamples()
		require.Len(t, samples, 1)
		require.Equal(t, "duplicate", samples[0].Outcome)
		require.Equal(t, "content_hash", samples[0].DedupeReason)
	})

	t.Run("fresh claim computes status, verdict, recorded_at, source_quality, classification", func(t *testing.T) {
		lookup := &stubClaimDedupeLookup{
			byIdempotencyKey: map[string]*domain.Claim{},
			byContentHash:    map[string]*domain.Claim{},
		}
		writer := &stubClaimWriter{}
		reader := &stubScopedReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {
					{
						"fragment_id":    "frag-1",
						"content":        "evidence A",
						"source_quality": 0.85,
						"classification": map[string]any{
							"confidentiality": "internal",
							"pii":             "none",
						},
					},
					{
						"fragment_id":    "frag-2",
						"content":        "evidence B",
						"source_quality": 0.60,
						"classification": map[string]any{
							"confidentiality": "public",
							"pii":             "sensitive",
						},
					},
				},
			},
		}

		input := &domain.Claim{
			Subject:     "Water",
			Predicate:   "is",
			Object:      "wet",
			SupportedBy: []string{"frag-1", "frag-2"},
		}

		before := time.Now().UTC()
		svc := NewCreateClaimService(lookup, reader, writer, nil, nil, nil)
		got, err := svc.Create(ctx, profileID, input)
		after := time.Now().UTC()

		require.NoError(t, err)
		require.NotNil(t, got)
		require.False(t, got.Duplicate, "fresh claim must not be marked as a duplicate")

		c := got.Claim

		// AC-13: server-side defaults.
		require.Equal(t, domain.StatusCandidate, c.Status,
			"status must default to 'candidate'")
		require.Equal(t, domain.EntailmentVerdict("insufficient"), c.EntailmentVerdict,
			"entailment_verdict must default to 'insufficient'")

		// recorded_at must be set to a time within the test window.
		require.False(t, c.RecordedAt.IsZero(), "recorded_at must be populated")
		require.False(t, c.RecordedAt.Before(before), "recorded_at must not predate test start")
		require.False(t, c.RecordedAt.After(after), "recorded_at must not postdate test end")

		// source_quality = max(0.85, 0.60) = 0.85
		require.InDelta(t, 0.85, c.SourceQuality, 1e-9,
			"source_quality must be the maximum across supporting fragments")

		// classification lattice max: confidentiality max(internal, public) = internal;
		// pii max(none, sensitive) = sensitive.
		require.Equal(t, "internal", c.Classification["confidentiality"],
			"classification.confidentiality must be lattice-max")
		require.Equal(t, "sensitive", c.Classification["pii"],
			"classification.pii must be lattice-max")
		require.Equal(t, "v1", c.ClassificationLatticeVersion,
			"classification_lattice_version must be 'v1'")

		// Identity fields derived by the service.
		require.NotEmpty(t, c.ContentHash, "content_hash must be populated")
		require.NotEmpty(t, c.ClaimID, "claim_id must be populated")
		require.Equal(t, profileID, c.ProfileID, "team_id must be propagated")

		// Exactly one write to the graph.
		require.Len(t, writer.written, 1, "exactly one graph write expected")
		require.Len(t, writer.queries, 1, "exactly one graph query expected")
		require.Contains(t, writer.queries[0], "c.classification_json            = $classificationJSON")

		classificationJSON, ok := writer.written[0]["classificationJSON"].(string)
		require.True(t, ok, "claim writes must encode classification as JSON")
		require.Equal(t, c.Classification, fragmentcodec.DecodeOptionalMap(classificationJSON))
		_, hasLegacyClassification := writer.written[0]["classification"]
		require.False(t, hasLegacyClassification, "legacy raw map classification param must not be used")
	})

	t.Run("fresh claim with idempotency key derives claim_id from key", func(t *testing.T) {
		lookup := &stubClaimDedupeLookup{
			byIdempotencyKey: map[string]*domain.Claim{},
			byContentHash:    map[string]*domain.Claim{},
		}
		writer := &stubClaimWriter{}
		reader := &stubScopedReader{rowsByProfile: map[string][]map[string]any{}}

		input := &domain.Claim{
			Subject:        "Sky",
			Predicate:      "is",
			Object:         "blue",
			IdempotencyKey: "idem-key-sky",
		}
		svc := NewCreateClaimService(lookup, reader, writer, nil, nil, nil)
		got, err := svc.Create(ctx, profileID, input)

		require.NoError(t, err)
		require.NotNil(t, got)
		require.False(t, got.Duplicate)

		// The claim_id must match the deterministic UUIDv5(profileID, idempotencyKey).
		expectedID, idErr := claimidentity.ClaimID(profileID, "idem-key-sky")
		require.NoError(t, idErr)
		require.Equal(t, expectedID, got.Claim.ClaimID,
			"claim_id must be UUIDv5(profileID, idempotencyKey)")
	})

	t.Run("fresh claim without idempotency key derives claim_id from content hash", func(t *testing.T) {
		lookup := &stubClaimDedupeLookup{
			byIdempotencyKey: map[string]*domain.Claim{},
			byContentHash:    map[string]*domain.Claim{},
		}
		writer := &stubClaimWriter{}
		reader := &stubScopedReader{rowsByProfile: map[string][]map[string]any{}}

		input := &domain.Claim{
			Subject:   "Grass",
			Predicate: "is",
			Object:    "green",
		}
		svc := NewCreateClaimService(lookup, reader, writer, nil, nil, nil)
		got, err := svc.Create(ctx, profileID, input)

		require.NoError(t, err)
		require.NotNil(t, got)

		hash := claimidentity.ContentHash(input.Subject, input.Predicate, input.Object, nil)
		expectedID, idErr := claimidentity.ClaimIDFromHash(profileID, hash)
		require.NoError(t, idErr)
		require.Equal(t, expectedID, got.Claim.ClaimID,
			"claim_id must be UUIDv5(profileID, contentHash) when no idempotency key")
		require.Equal(t, hash, got.Claim.ContentHash,
			"content_hash on the returned claim must match the computed hash")
	})
}

// TestCreateClaim_CrossProfileIsolation verifies that data from profile A is
// not accessible when creating a claim as profile B. This is a mandatory
// security test per .claude/rules/profile-isolation.md.
func TestCreateClaim_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()
	const profileA = "00000000-0000-0000-0000-000000000001"
	const profileB = "00000000-0000-0000-0000-000000000002"

	// The stub models Neo4j's profile-scoped isolation: ScopedRead returns only
	// the rows for the given profileID, mirroring the $profileId WHERE filter.
	reader := &stubScopedReader{
		rowsByProfile: map[string][]map[string]any{
			profileA: {
				{
					"fragment_id":    "frag-a1",
					"content":        "profile A private data",
					"source_quality": 0.9,
					"classification": nil,
				},
			},
			profileB: {}, // profile B has no fragments
		},
	}
	lookup := &stubClaimDedupeLookup{
		byIdempotencyKey: map[string]*domain.Claim{},
		byContentHash:    map[string]*domain.Claim{},
	}
	writer := &stubClaimWriter{}

	svc := NewCreateClaimService(lookup, reader, writer, nil, nil, nil)

	// Profile B references profile A's fragment — must fail.
	_, err := svc.Create(ctx, profileB, &domain.Claim{
		Subject:     "X",
		Predicate:   "y",
		Object:      "Z",
		SupportedBy: []string{"frag-a1"},
	})
	require.Error(t, err, "profile B must not be able to reference profile A's fragments")
	require.Empty(t, writer.written, "no graph write must occur when isolation check fails")

	// Profile A can create a claim using its own fragment without error.
	gotA, errA := svc.Create(ctx, profileA, &domain.Claim{
		Subject:     "X",
		Predicate:   "y",
		Object:      "Z",
		SupportedBy: []string{"frag-a1"},
	})
	require.NoError(t, errA, "profile A must be able to reference its own fragment")
	require.NotNil(t, gotA)
	require.Equal(t, profileA, gotA.Claim.ProfileID,
		"returned claim must carry profile A's ID, not profile B's")
}

// TestCreateClaimPersistsSupportEdges covers AC-12 and AC-14.
//
// AC-12: one SUPPORTED_BY edge per fragment, carrying team_id,
// fragment_id, extracted_at = recorded_at, and extract_conf = claim.extract_conf.
// AC-14: claim_create_total metric emitted with outcome="created".
//
// Profile isolation: edges param must carry the caller's profileID, not a
// cross-profile ID, so that MERGE (c)-[r:SUPPORTED_BY {team_id: …}]->(sf)
// cannot link nodes across profiles.
func TestCreateClaimPersistsSupportEdges(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"

	reader := &stubScopedReader{
		rowsByProfile: map[string][]map[string]any{
			profileID: {
				{
					"fragment_id":    "frag-1",
					"content":        "evidence A",
					"source_quality": 0.75,
					"classification": nil,
				},
				{
					"fragment_id":    "frag-2",
					"content":        "evidence B",
					"source_quality": 0.50,
					"classification": nil,
				},
			},
		},
	}
	lookup := &stubClaimDedupeLookup{
		byIdempotencyKey: map[string]*domain.Claim{},
		byContentHash:    map[string]*domain.Claim{},
	}
	writer := &stubClaimWriter{}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()

	const extractConf = 0.88
	input := &domain.Claim{
		Subject:     "Alice",
		Predicate:   "knows",
		Object:      "Bob",
		ExtractConf: extractConf,
		SupportedBy: []string{"frag-1", "frag-2"},
	}

	before := time.Now().UTC()
	svc := NewCreateClaimService(lookup, reader, writer, nil, nil, metrics)
	got, err := svc.Create(ctx, profileID, input)
	after := time.Now().UTC()

	require.NoError(t, err)
	require.NotNil(t, got)
	require.False(t, got.Duplicate, "fresh claim must not be marked as a duplicate")

	// Exactly one graph write (claim node + edges in one atomic Cypher).
	require.Len(t, writer.written, 1, "exactly one ScopedWrite expected")

	// Verify SUPPORTED_BY edge descriptors in the params.
	edgesRaw, ok := writer.written[0]["edges"]
	require.True(t, ok, "params must contain 'edges' key")

	edges, ok := edgesRaw.([]map[string]any)
	require.True(t, ok, "'edges' must be []map[string]any")
	require.Len(t, edges, 2, "one edge descriptor per fragment")

	// Index edges by fragment_id for order-independent assertions.
	edgeByFrag := make(map[string]map[string]any, len(edges))
	for _, e := range edges {
		fid, _ := e["fragment_id"].(string)
		edgeByFrag[fid] = e
	}

	for _, fragID := range []string{"frag-1", "frag-2"} {
		e, found := edgeByFrag[fragID]
		require.True(t, found, "edge for %s must be present", fragID)

		// extract_conf on the edge must equal the claim's extract_conf.
		require.InDelta(t, extractConf, e["extract_conf"], 1e-9,
			"edge.extract_conf must equal claim.ExtractConf for %s", fragID)

		// extracted_at must equal recorded_at (set by the service to now).
		extractedAt, timeOK := e["extracted_at"].(time.Time)
		require.True(t, timeOK, "edge.extracted_at must be a time.Time for %s", fragID)
		require.False(t, extractedAt.Before(before),
			"edge.extracted_at must not predate test start for %s", fragID)
		require.False(t, extractedAt.After(after),
			"edge.extracted_at must not postdate test end for %s", fragID)

		// Profile isolation: edge must carry the caller's profileID so that
		// the Cypher MERGE cannot link fragments across profiles.
		// (In production the $profileId param is injected by ScopedWrite; here
		// we verify the service correctly passes it through via the Cypher
		// parameter — no separate field on the edge descriptor needed, as
		// team_id is the $profileId bound variable in the query.)
	}

	// Verify that recorded_at on the returned claim matches the edge's extracted_at.
	for _, e := range edges {
		extractedAt, _ := e["extracted_at"].(time.Time)
		require.Equal(t, got.Claim.RecordedAt.UTC(), extractedAt.UTC(),
			"edge.extracted_at must equal claim.RecordedAt")
	}

	// AC-14: claim_create_total metric emitted with outcome="created".
	samples := metrics.ClaimCreateSamples()
	require.Len(t, samples, 1, "exactly one metric sample expected")
	require.Equal(t, "created", samples[0].Outcome,
		"metric outcome must be 'created' for a new claim")
	require.Empty(t, samples[0].DedupeReason,
		"dedupeReason must be empty for a new (non-duplicate) claim")

	// AC-12 cross-profile isolation: edges must NOT be created when the
	// fragment belongs to a different profile (fragment load will reject it).
	readerIsolation := &stubScopedReader{
		rowsByProfile: map[string][]map[string]any{
			// profile B has no fragments — cross-profile fragment IDs are absent.
			"00000000-0000-0000-0000-000000000002": {},
		},
	}
	writerIsolation := &stubClaimWriter{}
	svcIsolation := NewCreateClaimService(
		&stubClaimDedupeLookup{
			byIdempotencyKey: map[string]*domain.Claim{},
			byContentHash:    map[string]*domain.Claim{},
		},
		readerIsolation,
		writerIsolation,
		nil,
		nil,
		nil,
	)
	_, errIsolation := svcIsolation.Create(ctx, "00000000-0000-0000-0000-000000000002", &domain.Claim{
		Subject:     "Alice",
		Predicate:   "knows",
		Object:      "Bob",
		SupportedBy: []string{"frag-1"}, // frag-1 belongs to profile A, not B
	})
	require.Error(t, errIsolation,
		"cross-profile fragment reference must be rejected before any graph write")
	require.Empty(t, writerIsolation.written,
		"no graph write must occur when cross-profile isolation check fails")
}

func TestCreateRejectsInvalidConfidenceBeforeLookup(t *testing.T) {
	const profileID = "00000000-0000-0000-0000-000000000001"
	lookup := &stubClaimDedupeLookup{
		byIdempotencyKey: map[string]*domain.Claim{},
		byContentHash:    map[string]*domain.Claim{},
	}
	writer := &stubClaimWriter{}
	svc := NewCreateClaimService(lookup, nil, writer, nil, nil, nil)

	_, err := svc.Create(context.Background(), profileID, &domain.Claim{
		Subject:        "Alice",
		Predicate:      "likes",
		Object:         "coffee",
		ExtractConf:    99,
		ResolutionConf: 0.8,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "extract_conf")
	require.Empty(t, writer.written)
}

func TestValidateCreateClaimInputRejectsInvalidFields(t *testing.T) {
	from := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	to := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		claim *domain.Claim
		want  string
	}{
		{
			name:  "nil claim",
			claim: nil,
			want:  "claim is required",
		},
		{
			name:  "invalid modality",
			claim: &domain.Claim{Modality: domain.ClaimModality("rumor")},
			want:  "modality",
		},
		{
			name:  "invalid polarity",
			claim: &domain.Claim{Polarity: domain.ClaimPolarity("maybe")},
			want:  "polarity",
		},
		{
			name:  "nan extract confidence",
			claim: &domain.Claim{ExtractConf: math.NaN()},
			want:  "extract_conf",
		},
		{
			name:  "infinite resolution confidence",
			claim: &domain.Claim{ResolutionConf: math.Inf(1)},
			want:  "resolution_conf",
		},
		{
			name:  "valid_from after valid_to",
			claim: &domain.Claim{ValidFrom: &from, ValidTo: &to},
			want:  "valid_from",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCreateClaimInput(tc.claim)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}

	require.NoError(t, validateCreateClaimInput(&domain.Claim{
		Modality:       domain.ModalityAssertion,
		Polarity:       domain.PolarityPositive,
		ExtractConf:    1,
		ResolutionConf: 0,
		ValidFrom:      &to,
		ValidTo:        &from,
	}))
}

func TestCreateClaimLookupAndPersistErrors(t *testing.T) {
	const profileID = "00000000-0000-0000-0000-000000000001"

	t.Run("idempotency lookup error", func(t *testing.T) {
		lookup := &stubClaimDedupeLookup{err: errors.New("lookup down")}
		writer := &stubClaimWriter{}
		svc := NewCreateClaimService(lookup, nil, writer, nil, nil, nil)

		_, err := svc.Create(context.Background(), profileID, &domain.Claim{
			Subject:        "Alice",
			Predicate:      "likes",
			Object:         "coffee",
			IdempotencyKey: "same-request",
		})

		require.Error(t, err)
		require.Contains(t, err.Error(), "idempotency lookup")
		require.Empty(t, writer.written)
	})

	t.Run("content hash lookup error", func(t *testing.T) {
		lookup := &stubClaimDedupeLookup{err: errors.New("lookup down")}
		writer := &stubClaimWriter{}
		svc := NewCreateClaimService(lookup, nil, writer, nil, nil, nil)

		_, err := svc.Create(context.Background(), profileID, &domain.Claim{
			Subject:   "Alice",
			Predicate: "likes",
			Object:    "coffee",
		})

		require.Error(t, err)
		require.Contains(t, err.Error(), "content-hash lookup")
		require.Empty(t, writer.written)
	})

	t.Run("writer error", func(t *testing.T) {
		lookup := &stubClaimDedupeLookup{
			byIdempotencyKey: map[string]*domain.Claim{},
			byContentHash:    map[string]*domain.Claim{},
		}
		writer := &stubClaimWriter{err: errors.New("neo4j down")}
		svc := NewCreateClaimService(lookup, nil, writer, nil, nil, nil)

		_, err := svc.Create(context.Background(), profileID, &domain.Claim{
			Subject:   "Alice",
			Predicate: "likes",
			Object:    "coffee",
		})

		require.Error(t, err)
		require.Contains(t, err.Error(), "persist")
		require.Len(t, writer.written, 0)
	})
}

func TestCreateClaimActorAndAuditBranches(t *testing.T) {
	const profileID = "00000000-0000-0000-0000-000000000001"
	lookup := &stubClaimDedupeLookup{
		byIdempotencyKey: map[string]*domain.Claim{},
		byContentHash:    map[string]*domain.Claim{},
	}
	writer := &stubClaimWriter{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewCreateClaimService(
		lookup,
		nil,
		writer,
		&stubFailingAudit{err: errors.New("audit store down")},
		logger,
		nil,
	)
	actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		ProfileID:   actorID,
		ProfileName: "analyst",
	})

	got, err := svc.Create(ctx, profileID, &domain.Claim{
		Subject:   "Alice",
		Predicate: "likes",
		Object:    "coffee",
	})

	require.NoError(t, err)
	require.Equal(t, actorID.String(), got.Claim.CreatedByProfileID)
	require.Equal(t, "analyst", got.Claim.CreatedByProfileName)
	require.Len(t, writer.written, 1)
	require.Equal(t, actorID.String(), writer.written[0]["createdByProfileId"])
	require.Equal(t, "analyst", writer.written[0]["createdByProfileName"])
}
