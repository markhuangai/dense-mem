package main

import (
	"context"
	"strings"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type claimAuditAdapter struct {
	inner service.AuditService
}

func (a *claimAuditAdapter) Append(ctx context.Context, entry claimservice.AuditLogEntry) error {
	if a == nil || a.inner == nil {
		return nil
	}
	return a.inner.Append(ctx, service.AuditLogEntry{
		ProfileID:     stringPtr(entry.ProfileID),
		Timestamp:     entry.Timestamp,
		Operation:     entry.Operation,
		EntityType:    entry.EntityType,
		EntityID:      entry.EntityID,
		BeforePayload: entry.BeforePayload,
		AfterPayload:  entry.AfterPayload,
		ActorKeyID:    stringPtr(entry.ActorKeyID),
		ActorRole:     entry.ActorRole,
		ClientIP:      entry.ClientIP,
		CorrelationID: entry.CorrelationID,
		Metadata:      entry.Metadata,
	})
}

type factAuditAdapter struct {
	inner service.AuditService
}

func (a *factAuditAdapter) Append(ctx context.Context, entry factservice.AuditLogEntry) error {
	if a == nil || a.inner == nil {
		return nil
	}
	return a.inner.Append(ctx, service.AuditLogEntry{
		ProfileID:     stringPtr(entry.ProfileID),
		Timestamp:     entry.Timestamp,
		Operation:     entry.Operation,
		EntityType:    entry.EntityType,
		EntityID:      entry.EntityID,
		BeforePayload: entry.BeforePayload,
		AfterPayload:  entry.AfterPayload,
		ActorKeyID:    stringPtr(entry.ActorKeyID),
		ActorRole:     entry.ActorRole,
		ClientIP:      entry.ClientIP,
		CorrelationID: entry.CorrelationID,
		Metadata:      entry.Metadata,
	})
}

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

func verifierConfigured(cfg config.ConfigProvider) bool {
	return strings.TrimSpace(cfg.GetAIVerifierAPIURL()) != "" &&
		strings.TrimSpace(cfg.GetAIVerifierAPIKey()) != "" &&
		strings.TrimSpace(cfg.GetAIVerifierModel()) != ""
}

func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
