package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

// RecallHandler serves GET /api/v1/recall.
//
// The endpoint runs hybrid semantic + keyword recall for the caller's profile
// and returns ranked hits spanning all knowledge-pipeline tiers:
//   - tier "1"   active facts (highest authority)
//   - tier "1.5" validated claims
//   - tier "2"   raw SourceFragments (RRF-ranked)
//
// Profile isolation invariant: the profileID is always taken from the
// authenticated context set by ProfileResolutionMiddleware — never from
// caller-supplied query parameters.
type RecallHandler struct {
	svc recallservice.RecallService
}

// RecallHandlerInterface is the companion interface for RecallHandler.
type RecallHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure RecallHandler implements RecallHandlerInterface.
var _ RecallHandlerInterface = (*RecallHandler)(nil)

// NewRecallHandler constructs a RecallHandler.
func NewRecallHandler(svc recallservice.RecallService) *RecallHandler {
	return &RecallHandler{svc: svc}
}

// Handle handles GET /api/v1/recall.
// Query parameters: query (required, max 512), limit (optional, 0-50).
// Returns 200 with a {"data": [...]} body on success.
// Returns 400 when the query parameter is missing or invalid.
// Returns 503 when the embedding provider is unavailable.
func (h *RecallHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	var req dto.RecallRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid query parameters")
	}
	// Return 400 (not 422) for missing/invalid query param — stable external contract.
	if err := validation.ValidateStruct(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	hits, err := h.svc.Recall(ctx, profileID.String(), recallservice.RecallRequest{
		Query:           req.Query,
		Limit:           req.Limit,
		ValidAt:         req.ValidAt,
		KnownAt:         req.KnownAt,
		IncludeEvidence: req.IncludeEvidence,
		UseCommunities:  req.UseCommunities,
	})
	if err != nil {
		var apiErr *httperr.APIError
		if errors.As(err, &apiErr) {
			return apiErr
		}
		if errors.Is(err, recallservice.ErrEmbeddingUnavailable) {
			return httperr.New(httperr.SERVICE_UNAVAILABLE, "embedding provider unavailable")
		}
		if errors.Is(err, recallservice.ErrKeywordUnavailable) {
			return httperr.New(httperr.SERVICE_UNAVAILABLE, "keyword search unavailable")
		}
		if errors.Is(err, recallservice.ErrAssertionUnavailable) {
			return httperr.New(httperr.SERVICE_UNAVAILABLE, "semantic assertion search unavailable")
		}
		return httperr.New(httperr.INTERNAL_ERROR, "recall failed")
	}

	items := make([]dto.RecallHitResponse, 0, len(hits))
	for i := range hits {
		h2 := hits[i]
		item := dto.RecallHitResponse{
			Tier:         h2.Tier,
			Score:        h2.Score,
			SemanticRank: h2.SemanticRank,
			KeywordRank:  h2.KeywordRank,
			FinalScore:   h2.FinalScore,
			Assertion:    semanticAssertionResponse(h2.Assertion),
			Paths:        semanticPathResponses(h2.Paths),
			Frontier:     semanticFrontierResponses(h2.Frontier),
		}
		if h2.Fragment != nil {
			item.Fragment = response.ToFragmentResponse(h2.Fragment)
		}
		if h2.Claim != nil {
			item.Claim = response.ToClaimResponse(h2.Claim, "")
		}
		if h2.Fact != nil {
			item.Fact = response.ToFactResponse(h2.Fact)
		}
		items = append(items, item)
	}

	// Return hits wrapped in the standard {"data": [...]} envelope so callers
	// can use body.data consistently with other knowledge-pipeline endpoints.
	return response.SuccessOK(c, items)
}

func semanticPathResponses(paths []recallservice.SemanticPath) []dto.SemanticPathResponse {
	out := make([]dto.SemanticPathResponse, 0, len(paths))
	for _, path := range paths {
		converted := dto.SemanticPathResponse{
			Nodes: make([]dto.SemanticNodeResponse, 0, len(path.Nodes)),
			Edges: make([]dto.SemanticEdgeResponse, 0, len(path.Edges)),
		}
		for _, node := range path.Nodes {
			converted.Nodes = append(converted.Nodes, semanticNodeResponse(node))
		}
		for _, edge := range path.Edges {
			converted.Edges = append(converted.Edges, dto.SemanticEdgeResponse{
				AssertionID: edge.AssertionID, Source: edge.Source, Target: edge.Target,
				Relationship: edge.Relationship, Predicate: edge.Predicate, Tier: string(edge.Tier),
				Status: string(edge.Status), Polarity: string(edge.Polarity), ValidFrom: edge.ValidFrom,
				ValidTo: edge.ValidTo, EvidenceIDs: edge.EvidenceIDs,
			})
		}
		out = append(out, converted)
	}
	return out
}

func semanticFrontierResponses(hints []recallservice.FrontierHint) []dto.SemanticFrontierHintResponse {
	out := make([]dto.SemanticFrontierHintResponse, 0, len(hints))
	for _, hint := range hints {
		out = append(out, dto.SemanticFrontierHintResponse{
			FromEntityID: hint.FromEntityID, Direction: hint.Direction, Relationship: hint.Relationship,
			AssertionID: hint.AssertionID, Neighbor: semanticNodeResponse(hint.Neighbor), Tier: string(hint.Tier),
		})
	}
	return out
}

func semanticNodeResponse(node recallservice.SemanticNode) dto.SemanticNodeResponse {
	return dto.SemanticNodeResponse{Key: node.Key, ID: node.ID, Type: node.Type, Name: node.Name}
}

func semanticAssertionResponse(assertion *domain.Assertion) *dto.SemanticAssertionResponse {
	if assertion == nil {
		return nil
	}
	out := &dto.SemanticAssertionResponse{
		AssertionID: assertion.AssertionID, TeamID: assertion.ProfileID, OwnerProfileID: assertion.OwnerProfileID,
		SubjectEntityID: assertion.SubjectEntityID, PredicateKey: assertion.PredicateKey,
		RelationshipType: assertion.RelationshipType, ObjectEntityID: assertion.ObjectEntityID,
		Tier: string(assertion.Tier), Status: string(assertion.Status), PolicyFamily: string(assertion.PolicyFamily),
		Polarity: string(assertion.Polarity), Modality: string(assertion.Modality), ValidFrom: assertion.ValidFrom,
		ValidTo: assertion.ValidTo, RecordedAt: assertion.RecordedAt, RecordedTo: assertion.RecordedTo,
		ExtractConf: assertion.ExtractConf, ResolutionConf: assertion.ResolutionConf, SourceQuality: assertion.SourceQuality,
		SupportCount: assertion.SupportCount, SourceGroupCount: assertion.SourceGroupCount,
		Evidence:       make([]dto.SemanticEvidenceSpanResponse, 0, len(assertion.Evidence)),
		EmbeddingModel: assertion.EmbeddingModel, ExtractionModel: assertion.ExtractionModel,
		ExtractionVersion: assertion.ExtractionVersion, VerifierModel: assertion.VerifierModel,
		PipelineRunID: assertion.PipelineRunID, ProjectionVersion: assertion.ProjectionVersion,
		CreatedAt: assertion.CreatedAt, UpdatedAt: assertion.UpdatedAt,
	}
	if assertion.ObjectValue != nil {
		out.ObjectValue = &dto.SemanticTypedValueResponse{
			ValueID: assertion.ObjectValue.ValueID, ValueType: string(assertion.ObjectValue.ValueType),
			Value: assertion.ObjectValue.Value, Display: assertion.ObjectValue.Display, Unit: assertion.ObjectValue.Unit,
		}
	}
	for _, evidence := range assertion.Evidence {
		out.Evidence = append(out.Evidence, dto.SemanticEvidenceSpanResponse{
			FragmentID: evidence.FragmentID, Start: evidence.Start, End: evidence.End, SourceGroup: evidence.SourceGroup,
		})
	}
	return out
}
