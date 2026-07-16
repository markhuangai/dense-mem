package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

// stubRecallService implements recallservice.RecallService for testing.
// Mocks go in *_test.go only — cross-pkg consumers use a local stub per
// memory/feedback_mock_placement.md.
type stubRecallService struct {
	recallFunc func(ctx context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error)
}

func (s *stubRecallService) Recall(ctx context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	if s.recallFunc != nil {
		return s.recallFunc(ctx, profileID, req)
	}
	return nil, nil
}

// Compile-time check that stubRecallService satisfies RecallService.
var _ recallservice.RecallService = (*stubRecallService)(nil)

// recallDataEnvelope is the expected response shape for GET /api/v1/recall.
// The handler wraps hits in {"data": [...]} via response.SuccessOK (AC-55, AC-62).
type recallDataEnvelope struct {
	Data recallservice.PublicRecallResponse `json:"data"`
}

// TestRecallHandler verifies the handler returns 200 with recall hits wrapped in {"data": [...]}.
func TestRecallHandler(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	fragID := "frag-abc"
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
			return []recallservice.RecallHit{
				{
					Fragment:     &domain.Fragment{FragmentID: fragID, ProfileID: pid},
					Tier:         recallservice.TierFragment,
					Score:        0.9,
					SemanticRank: 1,
					KeywordRank:  2,
					FinalScore:   0.016,
				},
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/recall", h.Handle)

	// Use the "query" parameter (not "q") — stable external contract.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=test+query", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	var resp recallDataEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if len(resp.Data.Results) != 1 {
		t.Fatalf("result count = %d; want 1. body=%s", len(resp.Data.Results), rec.Body.String())
	}
	if resp.Data.Results[0].EvidenceID != fragID {
		t.Errorf("evidence_id mismatch: got result=%v; want evidence_id=%q", resp.Data.Results[0], fragID)
	}
	if resp.Data.DiscoveryGuidance != recallservice.DiscoveryGuidance {
		t.Errorf("discovery guidance = %q", resp.Data.DiscoveryGuidance)
	}
}

func TestRecallHandler_ReturnsClaimAndFactHits(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
			return []recallservice.RecallHit{
				{
					Claim: &domain.Claim{
						ClaimID:   "claim-abc",
						ProfileID: pid,
						Subject:   "sky",
						Predicate: "is",
						Object:    "blue",
						Status:    domain.StatusValidated,
					},
					Tier: recallservice.TierValidatedClaim,
				},
				{
					Fact: &domain.Fact{
						FactID:    "fact-abc",
						ProfileID: pid,
						Subject:   "water",
						Predicate: "is",
						Object:    "wet",
						Status:    domain.FactStatusActive,
					},
					Tier: recallservice.TierActiveFact,
				},
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=test+query", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	var resp recallDataEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if len(resp.Data.Results) != 2 {
		t.Fatalf("result count = %d; want 2. body=%s", len(resp.Data.Results), rec.Body.String())
	}
	if resp.Data.Results[0].EvidenceID != "claim-abc" || !strings.Contains(resp.Data.Results[0].Context, "blue") {
		t.Fatalf("claim result = %#v; want claim-abc", resp.Data.Results[0])
	}
	if resp.Data.Results[1].EvidenceID != "fact-abc" || !strings.Contains(resp.Data.Results[1].Context, "wet") {
		t.Fatalf("fact result = %#v; want fact-abc", resp.Data.Results[1])
	}
}

func TestRecallHandler_ReturnsSemanticRelationshipAndEvidenceHits(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
			return []recallservice.RecallHit{
				{
					Evidence: &domain.SemanticEvidenceFragment{
						TeamID:         pid,
						FragmentID:     "frag-support",
						OwnerProfileID: "owner-1",
						Content:        "Dense-Mem uses Postgres.",
						Source:         "eval",
						SourceDocID:    "doc-1",
						SourceType:     domain.SourceTypeConversation,
						Authority:      domain.AuthorityPrimary,
						ContentHash:    "hash-1",
						CreatedAt:      now,
					},
					Relationships: []domain.SemanticRelationship{{
						TeamID:            pid,
						RelationshipID:    "rel-abc",
						OwnerProfileID:    "owner-1",
						SubjectEntityID:   "entity-subject",
						SubjectEntityName: "Dense-Mem",
						Predicate:         "uses",
						ObjectEntityID:    "entity-object",
						ObjectValue:       "Postgres",
						ObjectKind:        "concept",
						Tier:              domain.SemanticTierValidatedClaim,
						Status:            domain.SemanticStatusActive,
						Confidence:        0.82,
						RecordedAt:        now,
						CreatedAt:         now,
						UpdatedAt:         now,
					}},
					Supports: []domain.SemanticRelationshipSupport{{
						TeamID:         pid,
						RelationshipID: "rel-abc",
						FragmentID:     "frag-support",
						EvidenceIndex:  2,
						Quote:          "Dense-Mem uses Postgres.",
						CreatedAt:      now,
					}},
					Tier:       string(domain.SemanticTierValidatedClaim),
					Score:      0.82,
					FinalScore: 0.82,
				},
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=v2+storage", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	var resp recallDataEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v. body=%s", err, rec.Body.String())
	}
	if len(resp.Data.Results) != 1 || len(resp.Data.DiscoveryPaths) != 1 {
		t.Fatalf("result/path counts = %d/%d; want 1/1. body=%s", len(resp.Data.Results), len(resp.Data.DiscoveryPaths), rec.Body.String())
	}
	result := resp.Data.Results[0]
	if result.EvidenceID != "frag-support" || result.Context == "" {
		t.Fatalf("evidence hit = %#v; want frag-support", result)
	}
	path := resp.Data.DiscoveryPaths[0]
	if len(path.Relationships) != 1 || path.Relationships[0].RelationshipID != "rel-abc" || path.Relationships[0].Predicate != "uses" {
		t.Fatalf("discovery path = %#v; want rel-abc uses", path)
	}
	for _, forbidden := range []string{"embedding-contract", "text-embedding-3-large", "hash-1", "idem-1"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

// TestRecallHandler_CrossProfileIsolation verifies that recall for profile B
// does not return results belonging to profile A. The service is responsible
// for scoping all DB queries to the injected profileID; the handler must never
// call the service with a different profileID than the one from the auth context.
func TestRecallHandler_CrossProfileIsolation(t *testing.T) {
	e := echo.New()
	profileB := uuid.New()

	fragAID := "frag-owned-by-a"
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
			// Simulate DB-level isolation: the service returns no results when
			// called with profile B's ID (profile A's data is invisible to B).
			if pid == profileB.String() {
				return nil, nil
			}
			return []recallservice.RecallHit{
				{Fragment: &domain.Fragment{FragmentID: fragAID, ProfileID: pid}, Tier: recallservice.TierFragment},
			}, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	// Inject profile B — profile A's fragments must not appear.
	e.Use(injectProfileMiddleware(profileB))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=anything", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	var resp recallDataEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, item := range resp.Data.Results {
		if item.EvidenceID == fragAID {
			t.Errorf("profile A fragment %q leaked into profile B recall results (isolation violation)", fragAID)
		}
	}
	// Explicitly verify profileB received no results (stub returns nil for B).
	if len(resp.Data.Results) != 0 {
		t.Errorf("profile B recall returned results; want none (cross-profile data must be invisible): %#v", resp.Data)
	}
}

func TestRecallHandler_ForwardsFollowUpContext(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	var captured recallservice.RecallRequest
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
			captured = req
			return nil, nil
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/recall", h.Handle)

	relationshipID := uuid.NewString()
	evidenceID := uuid.NewString()
	entityID := uuid.NewString()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=anything&known_evidence_ids="+evidenceID+"&known_relationship_ids="+relationshipID+"&expand_from_entity_ids="+entityID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200. body=%s", rec.Code, rec.Body.String())
	}
	if len(captured.KnownEvidenceIDs) != 1 || captured.KnownEvidenceIDs[0] != evidenceID {
		t.Fatalf("KnownEvidenceIDs = %#v; want %s", captured.KnownEvidenceIDs, evidenceID)
	}
	if len(captured.KnownRelationshipIDs) != 1 || captured.KnownRelationshipIDs[0] != relationshipID {
		t.Fatalf("KnownRelationshipIDs = %#v; want %s", captured.KnownRelationshipIDs, relationshipID)
	}
	if len(captured.ExpandFromEntityIDs) != 1 || captured.ExpandFromEntityIDs[0] != entityID {
		t.Fatalf("ExpandFromEntityIDs = %#v; want %s", captured.ExpandFromEntityIDs, entityID)
	}
}

// TestRecallHandler_Returns400WhenQueryMissing verifies missing query parameter → 400.
// Stable external contract: missing query returns 400 (Bad Request), not 422.
func TestRecallHandler_Returns400WhenQueryMissing(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	h := NewRecallHandler(&stubRecallService{})
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400. body=%s", rec.Code, rec.Body.String())
	}
}

// TestRecallHandler_Returns503WhenEmbeddingUnavailable verifies embedding failure → 503.
func TestRecallHandler_Returns503WhenEmbeddingUnavailable(t *testing.T) {
	e := echo.New()
	profileID := uuid.New()
	svc := &stubRecallService{
		recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
			return nil, recallservice.ErrEmbeddingUnavailable
		},
	}
	h := NewRecallHandler(svc)
	e.HTTPErrorHandler = httperr.ErrorHandler

	e.Use(injectProfileMiddleware(profileID))
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=hello", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503", rec.Code)
	}
}

func TestRecallHandler_ServiceErrorMappings(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "keyword unavailable",
			err:  recallservice.ErrKeywordUnavailable,
			want: http.StatusServiceUnavailable,
		},
		{
			name: "generic recall failure",
			err:  errors.New("query failed"),
			want: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			profileID := uuid.New()
			svc := &stubRecallService{
				recallFunc: func(ctx context.Context, pid string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
					return nil, tc.err
				},
			}
			h := NewRecallHandler(svc)
			e.HTTPErrorHandler = httperr.ErrorHandler

			e.Use(injectProfileMiddleware(profileID))
			e.GET("/api/v1/recall", h.Handle)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=hello", nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d; want %d. body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestRecallHandler_Returns400WhenProfileMissing verifies missing profile → 400.
func TestRecallHandler_Returns400WhenProfileMissing(t *testing.T) {
	e := echo.New()
	h := NewRecallHandler(&stubRecallService{})
	e.HTTPErrorHandler = httperr.ErrorHandler

	// No profile middleware injected.
	e.GET("/api/v1/recall", h.Handle)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=hello", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", rec.Code)
	}
}

// Compile-time companion interface check.
var _ RecallHandlerInterface = (*RecallHandler)(nil)
