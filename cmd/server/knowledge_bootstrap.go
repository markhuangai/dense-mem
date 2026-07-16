package main

import (
	"context"
	"strings"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/graphview"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type unavailableFragmentCreateService struct{}

func (unavailableFragmentCreateService) Create(context.Context, string, *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
	return nil, fragmentservice.ErrEmbeddingFailed
}

type unavailableRecallService struct{}

func (unavailableRecallService) Recall(context.Context, string, recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	return nil, recallservice.ErrEmbeddingUnavailable
}

type unavailableVerifyClaimService struct{}

func (unavailableVerifyClaimService) Verify(context.Context, string, string) (*domain.Claim, error) {
	return nil, verifier.ErrVerifierProvider
}

type unavailableCommunityDetectService struct{}

func (unavailableCommunityDetectService) Detect(context.Context, string, communityservice.DetectOptions) error {
	return communityservice.ErrCommunityUnavailable
}

type unavailableConfirmMemoryService struct{}

func (unavailableConfirmMemoryService) ConfirmMemory(context.Context, string, factservice.ConfirmMemoryRequest) (*factservice.ConfirmMemoryResult, error) {
	return nil, factservice.ErrClaimNotValidated
}

type unavailableGraphViewService struct{}

func (unavailableGraphViewService) Graph(_ context.Context, _ string, query graphview.Query) (*graphview.Snapshot, error) {
	scope := strings.TrimSpace(query.Scope)
	if scope == "" {
		scope = graphview.ScopeOverview
	}
	if scope == graphview.ScopeLocal && (strings.TrimSpace(query.AnchorType) == "" || strings.TrimSpace(query.AnchorID) == "") {
		return nil, graphview.ErrMissingAnchor
	}
	depth := query.Depth
	if depth <= 0 {
		depth = graphview.DefaultDepth
	}
	if depth > graphview.MaxDepth {
		depth = graphview.MaxDepth
	}
	limit := query.Limit
	if limit <= 0 {
		limit = graphview.DefaultLimit
	}
	if limit > graphview.MaxLimit {
		limit = graphview.MaxLimit
	}
	snapshot := &graphview.Snapshot{
		Scope: scope,
		Query: strings.TrimSpace(query.Query),
		Depth: depth,
		Limit: limit,
		Nodes: []graphview.Node{},
		Edges: []graphview.Edge{},
	}
	if scope == graphview.ScopeLocal {
		anchorType := strings.TrimSpace(query.AnchorType)
		anchorID := strings.TrimSpace(query.AnchorID)
		snapshot.Anchor = &graphview.Anchor{
			Type: anchorType,
			ID:   anchorID,
			Key:  anchorType + ":" + anchorID,
		}
	}
	return snapshot, nil
}

func (unavailableGraphViewService) NodeDetail(context.Context, string, string, string) (*graphview.Node, error) {
	return nil, graphview.ErrNodeNotFound
}

func verifierConfigured(cfg config.ConfigProvider) bool {
	return strings.TrimSpace(cfg.GetAIVerifierAPIURL()) != "" &&
		strings.TrimSpace(cfg.GetAIVerifierAPIKey()) != "" &&
		strings.TrimSpace(cfg.GetAIVerifierModel()) != ""
}
