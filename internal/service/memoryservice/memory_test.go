package memoryservice

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/stretchr/testify/require"
)

type stubFragmentCreate struct {
	called int
	req    *dto.CreateFragmentRequest
	res    *fragmentservice.CreateResult
	err    error
}

func (s *stubFragmentCreate) Create(_ context.Context, profileID string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
	s.called++
	s.req = req
	if s.err != nil {
		return nil, s.err
	}
	if s.res != nil {
		return s.res, nil
	}
	return &fragmentservice.CreateResult{
		Fragment: &domain.Fragment{
			FragmentID: "fragment-1",
			ProfileID:  profileID,
			Content:    req.Content,
			CreatedAt:  time.Now().UTC(),
		},
	}, nil
}

type stubClaimCreate struct {
	called int
	claim  *domain.Claim
	res    *claimservice.CreateResult
	err    error
}

func (s *stubClaimCreate) Create(_ context.Context, profileID string, claim *domain.Claim) (*claimservice.CreateResult, error) {
	s.called++
	s.claim = claim
	if s.err != nil {
		return nil, s.err
	}
	if s.res != nil {
		return s.res, nil
	}
	created := *claim
	created.ClaimID = "claim-1"
	created.ProfileID = profileID
	created.Status = domain.StatusCandidate
	return &claimservice.CreateResult{Claim: &created}, nil
}

type stubClaimVerify struct {
	called int
	claim  *domain.Claim
	err    error
}

func (s *stubClaimVerify) Verify(_ context.Context, profileID, claimID string) (*domain.Claim, error) {
	s.called++
	if s.err != nil {
		return nil, s.err
	}
	if s.claim != nil {
		return s.claim, nil
	}
	return &domain.Claim{
		ClaimID:           claimID,
		ProfileID:         profileID,
		Subject:           "user",
		Predicate:         "prefers",
		Object:            "vim",
		Status:            domain.StatusValidated,
		EntailmentVerdict: domain.VerdictEntailed,
	}, nil
}

type stubFactPromote struct {
	called int
	fact   *domain.Fact
	err    error
}

func (s *stubFactPromote) Promote(_ context.Context, profileID, claimID string) (*domain.Fact, error) {
	s.called++
	if s.err != nil {
		return nil, s.err
	}
	if s.fact != nil {
		return s.fact, nil
	}
	return &domain.Fact{FactID: "fact-1", ProfileID: profileID, PromotedFromClaimID: claimID}, nil
}

type stubFactList struct {
	facts []*domain.Fact
	err   error
}

func (s stubFactList) List(_ context.Context, _ string, _ factservice.FactListFilters, _ int, _ string) ([]*domain.Fact, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return s.facts, "", nil
}

type stubClaimList struct {
	claims []*domain.Claim
	err    error
}

func (s stubClaimList) List(_ context.Context, _ string, _ int, _ int) ([]*domain.Claim, int, error) {
	if s.err != nil {
		return nil, 0, s.err
	}
	return s.claims, len(s.claims), nil
}

type stubConfirm struct {
	called int
	res    *factservice.ConfirmMemoryResult
	err    error
}

func (s *stubConfirm) ConfirmMemory(_ context.Context, profileID string, req factservice.ConfirmMemoryRequest) (*factservice.ConfirmMemoryResult, error) {
	s.called++
	if s.err != nil {
		return nil, s.err
	}
	if s.res != nil {
		return s.res, nil
	}
	return &factservice.ConfirmMemoryResult{
		ClaimID:  req.ClaimID,
		Decision: req.Decision,
		Status:   "accepted",
		Fact:     &domain.Fact{FactID: "fact-confirmed", ProfileID: profileID},
	}, nil
}

func TestRememberPromotesValidatedNonConflictingClaim(t *testing.T) {
	create := &stubClaimCreate{}
	verify := &stubClaimVerify{}
	promote := &stubFactPromote{}
	svc := New(Dependencies{
		FragmentCreate: &stubFragmentCreate{},
		ClaimCreate:    create,
		ClaimVerify:    verify,
		FactPromote:    promote,
	})

	res, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
		Content: "The user prefers vim.",
		Claims: []TypedClaimInput{{
			Subject:        "user",
			Predicate:      "prefers",
			Object:         "vim",
			ExtractConf:    0.9,
			ResolutionConf: 0.9,
		}},
	})

	require.NoError(t, err)
	require.Equal(t, "fragment-1", res.Fragment.ID)
	require.Len(t, res.Claims, 1)
	require.Equal(t, "promoted", res.Claims[0].Promotion)
	require.Equal(t, "fact-1", res.Claims[0].Fact.FactID)
	require.Equal(t, []string{"fragment-1"}, create.claim.SupportedBy)
	require.Equal(t, 1, verify.called)
	require.Equal(t, 1, promote.called)
	require.Empty(t, res.Clarifications)
}

func TestRememberRejectsUnsupportedHighLevelPredicateBeforeClaimCreate(t *testing.T) {
	create := &stubClaimCreate{}
	svc := New(Dependencies{
		FragmentCreate: &stubFragmentCreate{},
		ClaimCreate:    create,
	})

	res, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
		Content: "The user lives at 123 Main St.",
		Claims: []TypedClaimInput{{
			Subject:        "user",
			Predicate:      "lives_at",
			Object:         "123 Main St",
			ExtractConf:    0.9,
			ResolutionConf: 0.9,
		}},
	})

	require.NoError(t, err)
	require.Len(t, res.Claims, 1)
	require.Equal(t, "predicate_not_supported", res.Claims[0].Status)
	require.Equal(t, 0, create.called)
}

func TestRememberRejectsInvalidTypedClaimBeforeClaimCreate(t *testing.T) {
	cases := []struct {
		name  string
		claim TypedClaimInput
		want  string
	}{
		{
			name: "extract confidence above one",
			claim: TypedClaimInput{
				Subject:        "user",
				Predicate:      "likes",
				Object:         "Go",
				ExtractConf:    99,
				ResolutionConf: 0.9,
			},
			want: "extract_conf",
		},
		{
			name: "invalid modality",
			claim: TypedClaimInput{
				Subject:        "user",
				Predicate:      "likes",
				Object:         "Go",
				Modality:       "belief",
				ExtractConf:    0.9,
				ResolutionConf: 0.9,
			},
			want: "modality",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fragmentCreate := &stubFragmentCreate{}
			claimCreate := &stubClaimCreate{}
			svc := New(Dependencies{
				FragmentCreate: fragmentCreate,
				ClaimCreate:    claimCreate,
			})

			res, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
				Content: "The user likes Go.",
				Claims:  []TypedClaimInput{tc.claim},
			})

			require.NoError(t, err)
			require.Equal(t, 1, fragmentCreate.called)
			require.Len(t, res.Claims, 1)
			require.Equal(t, "invalid", res.Claims[0].Status)
			require.Contains(t, res.Claims[0].Error, tc.want)
			require.Equal(t, 0, claimCreate.called)
		})
	}
}

func TestRememberReturnsStructuredPromotionOutcomes(t *testing.T) {
	t.Run("weaker conflict is rejected without clarification", func(t *testing.T) {
		svc := New(Dependencies{
			FragmentCreate: &stubFragmentCreate{},
			ClaimCreate:    &stubClaimCreate{},
			ClaimVerify:    &stubClaimVerify{},
			FactPromote:    &stubFactPromote{err: factservice.ErrPromotionRejected},
		})

		res, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
			Content: "The user has a different profile fact.",
			Claims: []TypedClaimInput{{
				Subject:        "user",
				Predicate:      "profile_fact",
				Object:         "new value",
				ExtractConf:    0.9,
				ResolutionConf: 0.9,
			}},
		})

		require.NoError(t, err)
		require.Equal(t, "rejected_weaker", res.Claims[0].Promotion)
		require.Empty(t, res.Clarifications)
	})

	t.Run("comparable conflict returns clarification", func(t *testing.T) {
		conflict := &domain.Fact{
			FactID:    "fact-old",
			ProfileID: "profile-1",
			Subject:   "user",
			Predicate: "profile_fact",
			Object:    "old value",
			Status:    domain.FactStatusActive,
		}
		verify := &stubClaimVerify{claim: &domain.Claim{
			ClaimID:           "claim-1",
			ProfileID:         "profile-1",
			Subject:           "user",
			Predicate:         "profile_fact",
			Object:            "new value",
			Status:            domain.StatusValidated,
			EntailmentVerdict: domain.VerdictEntailed,
		}}
		svc := New(Dependencies{
			FragmentCreate: &stubFragmentCreate{},
			ClaimCreate:    &stubClaimCreate{},
			ClaimVerify:    verify,
			FactPromote:    &stubFactPromote{err: factservice.ErrPromotionDeferredDisputed},
			FactList:       stubFactList{facts: []*domain.Fact{conflict}},
		})

		res, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
			Content: "The user has a conflicting profile fact.",
			Claims: []TypedClaimInput{{
				Subject:        "user",
				Predicate:      "profile_fact",
				Object:         "new value",
				ExtractConf:    0.9,
				ResolutionConf: 0.9,
			}},
		})

		require.NoError(t, err)
		require.Equal(t, "clarification_required", res.Claims[0].Promotion)
		require.Len(t, res.Clarifications, 1)
		require.Equal(t, "claim-1", res.Clarifications[0].ClaimID)
		require.Equal(t, "fact-old", res.Clarifications[0].ConflictingFacts[0].FactID)
	})
}

func TestImportMemoriesDoesNotAutoPromoteByDefault(t *testing.T) {
	promote := &stubFactPromote{}
	svc := New(Dependencies{
		FragmentCreate: &stubFragmentCreate{},
		ClaimCreate:    &stubClaimCreate{},
		ClaimVerify:    &stubClaimVerify{},
		FactPromote:    promote,
	})

	res, err := svc.ImportMemories(context.Background(), "profile-1", ImportRequest{
		Summary: "Historical summary: user uses Go.",
		Claims: []TypedClaimInput{{
			Subject:        "user",
			Predicate:      "uses",
			Object:         "Go",
			ExtractConf:    0.9,
			ResolutionConf: 0.9,
		}},
	})

	require.NoError(t, err)
	require.Len(t, res.Claims, 1)
	require.Empty(t, res.Claims[0].Promotion)
	require.Equal(t, 0, promote.called)
}

func TestReflectReturnsDisputedClarificationsAndStaleFacts(t *testing.T) {
	old := time.Now().Add(-60 * 24 * time.Hour)
	disputed := &domain.Claim{
		ClaimID:   "claim-disputed",
		Subject:   "user",
		Predicate: "profile_fact",
		Object:    "new value",
		Status:    domain.StatusDisputed,
	}
	svc := New(Dependencies{
		FactList: stubFactList{facts: []*domain.Fact{{
			FactID:     "fact-stale",
			Subject:    "user",
			Predicate:  "profile_fact",
			Object:     "old value",
			Status:     domain.FactStatusActive,
			RecordedAt: old,
		}}},
		ClaimList: stubClaimList{claims: []*domain.Claim{
			{ClaimID: "claim-candidate", Status: domain.StatusCandidate},
			disputed,
		}},
	})

	res, err := svc.Reflect(context.Background(), "profile-1", ReflectRequest{StaleAfterDays: 30})

	require.NoError(t, err)
	require.Len(t, res.StaleFacts, 1)
	require.Len(t, res.CandidateClaims, 1)
	require.Len(t, res.DisputedClaims, 1)
	require.Len(t, res.Clarifications, 1)
	require.Equal(t, "claim-disputed", res.Clarifications[0].ClaimID)
}

func TestConfirmMemoryDelegatesConfirmation(t *testing.T) {
	confirm := &stubConfirm{}
	svc := New(Dependencies{FactConfirm: confirm})

	res, err := svc.ConfirmMemory(context.Background(), "profile-1", ConfirmRequest{
		ClaimID:  "claim-1",
		Decision: "accept_claim",
	})

	require.NoError(t, err)
	require.Equal(t, 1, confirm.called)
	require.Equal(t, "accepted", res.Status)
	require.Equal(t, "fact-confirmed", res.Fact.FactID)
}

func TestRememberReturnsFragmentCreateError(t *testing.T) {
	svc := New(Dependencies{FragmentCreate: &stubFragmentCreate{err: errors.New("embed failed")}})

	_, err := svc.Remember(context.Background(), "profile-1", RememberRequest{Content: "x"})

	require.ErrorContains(t, err, "embed failed")
}

func TestRememberValidatesRequiredDependenciesAndContent(t *testing.T) {
	t.Run("missing fragment create service", func(t *testing.T) {
		svc := New(Dependencies{})

		_, err := svc.Remember(context.Background(), "profile-1", RememberRequest{Content: "memory"})

		require.ErrorContains(t, err, "fragment create service is required")
	})

	t.Run("empty content", func(t *testing.T) {
		svc := New(Dependencies{FragmentCreate: &stubFragmentCreate{}})

		_, err := svc.Remember(context.Background(), "profile-1", RememberRequest{})

		require.ErrorContains(t, err, "content is required")
	})
}

func TestRememberMapsFragmentRequestAndDuplicateOutcome(t *testing.T) {
	fragmentCreate := &stubFragmentCreate{res: &fragmentservice.CreateResult{
		Fragment: &domain.Fragment{
			FragmentID: "fragment-dup",
			ProfileID:  "profile-1",
			Content:    "memory",
			CreatedAt:  time.Now().UTC(),
		},
		Duplicate:   true,
		DuplicateOf: "fragment-old",
	}}
	autoPromote := false
	svc := New(Dependencies{FragmentCreate: fragmentCreate})

	res, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
		Content:        "memory",
		Source:         "chat",
		IdempotencyKey: "idem-1",
		Labels:         []string{"work"},
		Metadata:       map[string]any{"channel": "cli"},
		AutoPromote:    &autoPromote,
	})

	require.NoError(t, err)
	require.Equal(t, "duplicate", res.Fragment.Status)
	require.Equal(t, "fragment-old", res.Fragment.DuplicateOf)
	require.Equal(t, "conversation", fragmentCreate.req.SourceType)
	require.Equal(t, "primary", fragmentCreate.req.Authority)
	require.Equal(t, "chat", fragmentCreate.req.Source)
	require.Equal(t, "idem-1", fragmentCreate.req.IdempotencyKey)
	require.Equal(t, []string{"work"}, fragmentCreate.req.Labels)
	require.Equal(t, map[string]any{"channel": "cli"}, fragmentCreate.req.Metadata)
	require.Equal(t, 0.95, fragmentCreate.req.SourceQuality)
}

func TestImportMemoriesAutoPromotesWhenRequested(t *testing.T) {
	autoPromote := true
	promote := &stubFactPromote{}
	fragmentCreate := &stubFragmentCreate{}
	svc := New(Dependencies{
		FragmentCreate: fragmentCreate,
		ClaimCreate:    &stubClaimCreate{},
		ClaimVerify:    &stubClaimVerify{},
		FactPromote:    promote,
	})

	res, err := svc.ImportMemories(context.Background(), "profile-1", ImportRequest{
		Summary:     "Historical summary: user uses Go.",
		AutoPromote: &autoPromote,
		Claims: []TypedClaimInput{{
			Subject:        "user",
			Predicate:      "uses",
			Object:         "Go",
			ExtractConf:    0.9,
			ResolutionConf: 0.9,
		}},
	})

	require.NoError(t, err)
	require.Equal(t, "promoted", res.Claims[0].Promotion)
	require.Equal(t, 1, promote.called)
	require.Equal(t, "document", fragmentCreate.req.SourceType)
	require.Equal(t, "secondary", fragmentCreate.req.Authority)
	require.Equal(t, 0.75, fragmentCreate.req.SourceQuality)
}

func TestRememberReturnsPerClaimErrorsWithoutCreatingClaims(t *testing.T) {
	cases := []struct {
		name string
		deps Dependencies
		in   TypedClaimInput
		want string
	}{
		{
			name: "missing subject object or predicate",
			deps: Dependencies{ClaimCreate: &stubClaimCreate{}},
			in: TypedClaimInput{
				Predicate:      "likes",
				Object:         "Go",
				ExtractConf:    0.9,
				ResolutionConf: 0.9,
			},
			want: "subject, predicate, and object are required",
		},
		{
			name: "missing claim create service",
			deps: Dependencies{},
			in: TypedClaimInput{
				Subject:        "user",
				Predicate:      "likes",
				Object:         "Go",
				ExtractConf:    0.9,
				ResolutionConf: 0.9,
			},
			want: "claim create service is required",
		},
		{
			name: "claim create error",
			deps: Dependencies{ClaimCreate: &stubClaimCreate{err: errors.New("claim write failed")}},
			in: TypedClaimInput{
				Subject:        "user",
				Predicate:      "likes",
				Object:         "Go",
				ExtractConf:    0.9,
				ResolutionConf: 0.9,
			},
			want: "claim write failed",
		},
		{
			name: "claim verify error",
			deps: Dependencies{
				ClaimCreate: &stubClaimCreate{},
				ClaimVerify: &stubClaimVerify{err: errors.New("verification failed")},
			},
			in: TypedClaimInput{
				Subject:        "user",
				Predicate:      "likes",
				Object:         "Go",
				ExtractConf:    0.9,
				ResolutionConf: 0.9,
			},
			want: "verification failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.deps.FragmentCreate = &stubFragmentCreate{}
			svc := New(tc.deps)

			res, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
				Content: "The user likes Go.",
				Claims:  []TypedClaimInput{tc.in},
			})

			require.NoError(t, err)
			require.Len(t, res.Claims, 1)
			require.Contains(t, res.Claims[0].Error, tc.want)
		})
	}
}

func TestRememberDoesNotPromoteUnvalidatedClaim(t *testing.T) {
	promote := &stubFactPromote{}
	svc := New(Dependencies{
		FragmentCreate: &stubFragmentCreate{},
		ClaimCreate:    &stubClaimCreate{},
		ClaimVerify: &stubClaimVerify{claim: &domain.Claim{
			ClaimID:           "claim-1",
			Status:            domain.StatusCandidate,
			EntailmentVerdict: domain.VerdictInsufficient,
		}},
		FactPromote: promote,
	})

	res, err := svc.Remember(context.Background(), "profile-1", RememberRequest{
		Content: "The user likes Go.",
		Claims: []TypedClaimInput{{
			Subject:        "user",
			Predicate:      "likes",
			Object:         "Go",
			ExtractConf:    0.9,
			ResolutionConf: 0.9,
		}},
	})

	require.NoError(t, err)
	require.Equal(t, string(domain.StatusCandidate), res.Claims[0].Status)
	require.Empty(t, res.Claims[0].Promotion)
	require.Equal(t, 0, promote.called)
}

func TestClaimFromInputPreservesExplicitFields(t *testing.T) {
	validFrom := time.Now().UTC().Add(-time.Hour)
	validTo := time.Now().UTC().Add(time.Hour)
	in := TypedClaimInput{
		Subject:           "user",
		Predicate:         "likes",
		Object:            "Go",
		Modality:          string(domain.ModalityQuoted),
		Polarity:          string(domain.PolarityMinus),
		Speaker:           "assistant",
		ExtractConf:       0.8,
		ResolutionConf:    0.7,
		IdempotencyKey:    "idem",
		ValidFrom:         &validFrom,
		ValidTo:           &validTo,
		SupportedBy:       []string{"fragment-a", "fragment-b"},
		ExtractionModel:   "extractor",
		ExtractionVersion: "v1",
		PipelineRunID:     "run-1",
		Classification:    map[string]any{"kind": "preference"},
	}

	claim := claimFromInput(in, "fallback-fragment")

	require.Equal(t, domain.ModalityQuoted, claim.Modality)
	require.Equal(t, domain.PolarityMinus, claim.Polarity)
	require.Equal(t, []string{"fragment-a", "fragment-b"}, claim.SupportedBy)
	require.Equal(t, "assistant", claim.Speaker)
	require.Equal(t, "idem", claim.IdempotencyKey)
	require.Equal(t, &validFrom, claim.ValidFrom)
	require.Equal(t, &validTo, claim.ValidTo)
	require.Equal(t, "extractor", claim.ExtractionModel)
	require.Equal(t, "v1", claim.ExtractionVersion)
	require.Equal(t, "run-1", claim.PipelineRunID)
	require.Equal(t, map[string]any{"kind": "preference"}, claim.Classification)
}

func TestPromotionStatusMapsKnownErrors(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{factservice.ErrPromotionDeferredDisputed, "clarification_required"},
		{factservice.ErrPromotionRejected, "rejected_weaker"},
		{factservice.ErrGateRejected, "gate_rejected"},
		{factservice.ErrPredicateNotPoliced, "predicate_not_supported"},
		{factservice.ErrClaimNotValidated, "not_validated"},
		{errors.New("unexpected"), "error"},
	}

	for _, tc := range cases {
		require.Equal(t, tc.want, promotionStatus(tc.err))
	}
}

func TestValidateTypedClaimInputRejectsAllInvalidFields(t *testing.T) {
	valid := TypedClaimInput{
		Subject:        "user",
		Predicate:      "likes",
		Object:         "Go",
		ExtractConf:    0.9,
		ResolutionConf: 0.9,
	}
	cases := []struct {
		name string
		edit func(*TypedClaimInput)
		want string
	}{
		{"blank subject", func(in *TypedClaimInput) { in.Subject = " " }, "subject is required"},
		{"long subject", func(in *TypedClaimInput) { in.Subject = strings.Repeat("s", 257) }, "subject exceeds maximum length"},
		{"blank predicate", func(in *TypedClaimInput) { in.Predicate = " " }, "predicate is required"},
		{"blank object", func(in *TypedClaimInput) { in.Object = " " }, "object is required"},
		{"long object", func(in *TypedClaimInput) { in.Object = strings.Repeat("o", 1025) }, "object exceeds maximum length"},
		{"long speaker", func(in *TypedClaimInput) { in.Speaker = strings.Repeat("s", 257) }, "speaker exceeds maximum length"},
		{"long idempotency key", func(in *TypedClaimInput) { in.IdempotencyKey = strings.Repeat("i", 129) }, "idempotency_key exceeds maximum length"},
		{"long extraction model", func(in *TypedClaimInput) { in.ExtractionModel = strings.Repeat("m", 129) }, "extraction_model exceeds maximum length"},
		{"long extraction version", func(in *TypedClaimInput) { in.ExtractionVersion = strings.Repeat("v", 65) }, "extraction_version exceeds maximum length"},
		{"long pipeline run id", func(in *TypedClaimInput) { in.PipelineRunID = strings.Repeat("p", 129) }, "pipeline_run_id exceeds maximum length"},
		{"invalid modality", func(in *TypedClaimInput) { in.Modality = "belief" }, "modality must be one of"},
		{"invalid polarity", func(in *TypedClaimInput) { in.Polarity = "0" }, "polarity must be one of"},
		{"invalid extract conf", func(in *TypedClaimInput) { in.ExtractConf = -0.1 }, "extract_conf must be between 0 and 1"},
		{"invalid resolution conf", func(in *TypedClaimInput) { in.ResolutionConf = 1.1 }, "resolution_conf must be between 0 and 1"},
	}

	require.NoError(t, validateTypedClaimInput(valid))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := valid
			tc.edit(&in)

			err := validateTypedClaimInput(in)

			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestMemoryServiceHelpersCoverBoundaryValues(t *testing.T) {
	require.True(t, validConfidence(0))
	require.True(t, validConfidence(1))
	require.False(t, validConfidence(-0.01))
	require.False(t, validConfidence(1.01))
	require.False(t, validConfidence(math.NaN()))
	require.False(t, validConfidence(math.Inf(1)))

	require.Equal(t, defaultReflectLimit, clampReflectLimit(0))
	require.Equal(t, defaultReflectLimit, clampReflectLimit(-1))
	require.Equal(t, 50, clampReflectLimit(50))
	require.Equal(t, maxReflectLimit, clampReflectLimit(maxReflectLimit+1))

	require.Equal(t, 0.95, defaultSourceQuality("authoritative"))
	require.Equal(t, 0.5, defaultSourceQuality("unknown"))

	require.False(t, isStale(nil, 30))
	require.False(t, isStale(&domain.Fact{}, 30))
	require.False(t, isStale(&domain.Fact{RecordedAt: time.Now().UTC().Add(-48 * time.Hour)}, 0))
	require.False(t, isStale(&domain.Fact{RecordedAt: time.Now().UTC()}, 30))
	require.True(t, isStale(&domain.Fact{RecordedAt: time.Now().UTC().Add(-31 * 24 * time.Hour)}, 30))
}

func TestReflectReturnsDependencyErrors(t *testing.T) {
	t.Run("fact list error", func(t *testing.T) {
		svc := New(Dependencies{FactList: stubFactList{err: errors.New("fact list failed")}})

		_, err := svc.Reflect(context.Background(), "profile-1", ReflectRequest{})

		require.ErrorContains(t, err, "fact list failed")
	})

	t.Run("claim list error", func(t *testing.T) {
		svc := New(Dependencies{ClaimList: stubClaimList{err: errors.New("claim list failed")}})

		_, err := svc.Reflect(context.Background(), "profile-1", ReflectRequest{})

		require.ErrorContains(t, err, "claim list failed")
	})
}

func TestConfirmMemoryReturnsDependencyErrors(t *testing.T) {
	t.Run("missing confirm service", func(t *testing.T) {
		svc := New(Dependencies{})

		_, err := svc.ConfirmMemory(context.Background(), "profile-1", ConfirmRequest{ClaimID: "claim-1"})

		require.ErrorContains(t, err, "fact confirm service is required")
	})

	t.Run("confirm service error", func(t *testing.T) {
		svc := New(Dependencies{FactConfirm: &stubConfirm{err: errors.New("confirm failed")}})

		_, err := svc.ConfirmMemory(context.Background(), "profile-1", ConfirmRequest{ClaimID: "claim-1"})

		require.ErrorContains(t, err, "confirm failed")
	})
}
