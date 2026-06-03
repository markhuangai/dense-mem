// Package fragmentservice provides fragment creation and management services.
//
// SYNCHRONOUS EMBEDDING CONTRACT (AC-52):
// This service implements synchronous embedding on the create path. The embedding is
// generated before persistence, and failure to embed prevents the write entirely.
// This ensures no fragment exists without an embedding (AC-23).
//
// ASYNC EMBEDDING (DEFERRED):
// An asynchronous embedding path is a future design option. If implemented, it would
// require a separate "pending" state for fragments and a background worker to process
// them. This is not currently implemented.
package fragmentservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	httpvalidation "github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
	"github.com/markhuangai/dense-mem/internal/service/fragmentdedupe"
	"github.com/markhuangai/dense-mem/internal/service/fragmentidentity"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

// Errors for the create fragment service.
var (
	// ErrEmbeddingFailed indicates that embedding generation failed.
	ErrEmbeddingFailed = errors.New("failed to generate embedding for fragment")
	// ErrVectorLengthMismatch indicates that the returned embedding vector has wrong dimensions.
	ErrVectorLengthMismatch = errors.New("embedding vector length mismatch")
)

// createFragmentService implements CreateFragmentService with synchronous embedding.
type createFragmentService struct {
	embed       embedding.EmbeddingProviderInterface
	writer      neo4j.ScopedWriter
	lookup      fragmentdedupe.DedupeLookup
	audit       AuditEmitter
	consistency EmbeddingConsistencyChecker
	logger      *slog.Logger
	metrics     observability.DiscoverabilityMetrics
}

// Compile-time assertion that createFragmentService implements CreateFragmentService.
var _ CreateFragmentService = (*createFragmentService)(nil)

// AuditEmitter defines the interface for emitting audit events.
// This is a subset of AuditService focused on fragment creation needs.
type AuditEmitter interface {
	Append(ctx context.Context, entry AuditLogEntry) error
}

// AuditLogEntry represents an audit log entry for the fragment service.
// This mirrors the service.AuditLogEntry structure to avoid import cycles.
type AuditLogEntry struct {
	ID            string
	ProfileID     *string
	Timestamp     time.Time
	Operation     string
	EntityType    string
	EntityID      string
	AfterPayload  map[string]interface{}
	ActorKeyID    *string
	ActorRole     string
	ClientIP      string
	CorrelationID string
	Metadata      map[string]interface{}
}

// EmbeddingConsistencyChecker defines the interface for embedding consistency validation.
type EmbeddingConsistencyChecker interface {
	// ValidateVectorLength checks that an embedding vector has the expected dimensions.
	ValidateVectorLength(vec []float32) error
	// RecordFirstWrite initializes the embedding config on first successful write.
	RecordFirstWrite(ctx context.Context, model string, dimensions int) error
}

// NewCreateFragmentService creates a new fragment creation service.
// metrics may be nil; a noop recorder is substituted so call sites need no nil checks.
func NewCreateFragmentService(
	embed embedding.EmbeddingProviderInterface,
	writer neo4j.ScopedWriter,
	lookup fragmentdedupe.DedupeLookup,
	audit AuditEmitter,
	consistency EmbeddingConsistencyChecker,
	logger *slog.Logger,
	metrics observability.DiscoverabilityMetrics,
) CreateFragmentService {
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &createFragmentService{
		embed:       embed,
		writer:      writer,
		lookup:      lookup,
		audit:       audit,
		consistency: consistency,
		logger:      logger,
		metrics:     metrics,
	}
}

// Create creates a new fragment with server-side embedding.
// The algorithm:
// 1. Set default source type if not provided (AC-46).
// 2. Compute content hash for deduplication (AC-20).
// 3. Check idempotency key deduplication (AC-21).
// 4. Check content hash deduplication (AC-22).
// 5. Generate embedding (AC-23, AC-49).
// 6. Validate vector length (AC-47).
// 7. Build fragment domain object.
// 8. Persist via ScopedWrite (AC-24).
// 9. Record first-write consistency (AC-52).
// 10. Emit audit event (AC-26).
func (s *createFragmentService) Create(ctx context.Context, profileID string, req *dto.CreateFragmentRequest) (*CreateResult, error) {
	if err := validateCreateFragmentRequest(req); err != nil {
		s.metrics.IncFragmentCreate("error")
		return nil, err
	}

	// Step 1: Set default source type (AC-46)
	sourceType := domain.SourceType(req.SourceType)
	if sourceType == "" {
		sourceType = domain.SourceTypeManual
	}
	authority := domain.Authority(req.Authority)
	if authority == "" {
		authority = domain.AuthorityUnknown
	}

	// Step 2: Compute content hash (AC-20)
	contentHash := fragmentidentity.ContentHash(req.Content).Hex

	// Step 3: Check idempotency key deduplication (AC-21)
	if req.IdempotencyKey != "" {
		existing, err := s.lookup.ByIdempotencyKey(ctx, profileID, req.IdempotencyKey)
		if err != nil {
			if s.logger != nil {
				s.logger.Error("fragment create: idempotency lookup failed",
					slog.String("team_id", profileID),
					slog.String("error", err.Error()),
				)
			}
			s.metrics.IncFragmentCreate("error")
			return nil, fmt.Errorf("failed to check idempotency key: %w", err)
		}
		if existing != nil {
			s.metrics.IncFragmentCreate("duplicate")
			return &CreateResult{
				Fragment:    existing,
				Duplicate:   true,
				DuplicateOf: existing.FragmentID,
			}, nil
		}
	} else {
		// Step 4: Check content hash deduplication (AC-22)
		existing, err := s.lookup.ByContentHash(ctx, profileID, contentHash)
		if err != nil {
			if s.logger != nil {
				s.logger.Error("fragment create: content-hash lookup failed",
					slog.String("team_id", profileID),
					slog.String("content_hash", contentHash),
					slog.String("error", err.Error()),
				)
			}
			s.metrics.IncFragmentCreate("error")
			return nil, fmt.Errorf("failed to check content hash: %w", err)
		}
		if existing != nil {
			s.metrics.IncFragmentCreate("duplicate")
			return &CreateResult{
				Fragment:    existing,
				Duplicate:   true,
				DuplicateOf: existing.FragmentID,
			}, nil
		}
	}

	// Step 5: Generate embedding (AC-23, AC-49)
	// Server-side embedding - explicitly ignore any client-supplied embedding
	vec, model, err := s.embed.Embed(ctx, req.Content)
	if err != nil {
		// Embedding failure prevents persistence (AC-23)
		if s.logger != nil {
			s.logger.Error("embedding generation failed",
				slog.String("team_id", profileID),
				slog.String("error", err.Error()),
			)
		}
		s.metrics.IncFragmentCreate("error")
		return nil, errors.Join(ErrEmbeddingFailed, err)
	}

	// Step 6: Validate vector length (AC-47)
	dims := len(vec)
	if s.consistency != nil {
		if err := s.consistency.ValidateVectorLength(vec); err != nil {
			if s.logger != nil {
				s.logger.Error("embedding vector length mismatch",
					slog.String("team_id", profileID),
					slog.Int("expected", s.embed.Dimensions()),
					slog.Int("actual", dims),
				)
			}
			s.metrics.IncFragmentCreate("error")
			return nil, fmt.Errorf("%w: %v", ErrVectorLengthMismatch, err)
		}
	}

	// Step 7: Build fragment domain object
	now := time.Now().UTC()
	fragmentID := fragmentidentity.NewFragmentID()
	ownerID, ownerName, _ := requestctx.ActorOwner(ctx)

	fragment := &domain.Fragment{
		FragmentID:           fragmentID,
		ProfileID:            profileID,
		OwnerProfileID:       ownerID,
		OwnerProfileName:     ownerName,
		CreatedByProfileID:   ownerID,
		CreatedByProfileName: ownerName,
		Content:              req.Content,
		Source:               req.Source,
		SourceType:           sourceType,
		Authority:            authority,
		Labels:               req.Labels,
		Metadata:             req.Metadata,
		ContentHash:          contentHash,
		IdempotencyKey:       req.IdempotencyKey,
		EmbeddingModel:       model,
		EmbeddingDimensions:  dims,
		SourceQuality:        req.SourceQuality,
		Classification:       req.Classification,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	metadataJSON, err := fragmentcodec.EncodeOptionalMap(fragment.Metadata)
	if err != nil {
		s.metrics.IncFragmentCreate("error")
		return nil, fmt.Errorf("failed to encode fragment metadata: %w", err)
	}
	classificationJSON, err := fragmentcodec.EncodeOptionalMap(fragment.Classification)
	if err != nil {
		s.metrics.IncFragmentCreate("error")
		return nil, fmt.Errorf("failed to encode fragment classification: %w", err)
	}

	// Step 8: Persist via ScopedWrite (AC-24)
	// Note: embedding vector is stored but not exposed in read responses (AC-28)
	query := `
		CREATE (sf:SourceFragment {
			team_id: $profileId,
			fragment_id: $fragmentId,
			content: $content,
			content_hash: $contentHash,
			idempotency_key: $idempotencyKey,
			source: $source,
			source_type: $sourceType,
			authority: $authority,
			labels: $labels,
			metadata_json: $metadataJSON,
			embedding: $embedding,
			embedding_model: $embeddingModel,
			embedding_dimensions: $embeddingDimensions,
			source_quality: $sourceQuality,
			classification_json: $classificationJSON,
			owner_profile_id: $ownerProfileId,
			owner_profile_name: $ownerProfileName,
			created_by_profile_id: $createdByProfileId,
			created_by_profile_name: $createdByProfileName,
			created_at: $createdAt,
			updated_at: $updatedAt
		})
	`

	params := map[string]any{
		"fragmentId":           fragment.FragmentID,
		"content":              fragment.Content,
		"contentHash":          fragment.ContentHash,
		"idempotencyKey":       fragment.IdempotencyKey,
		"source":               fragment.Source,
		"sourceType":           string(fragment.SourceType),
		"authority":            string(fragment.Authority),
		"labels":               fragment.Labels,
		"metadataJSON":         metadataJSON,
		"embedding":            vec,
		"embeddingModel":       fragment.EmbeddingModel,
		"embeddingDimensions":  fragment.EmbeddingDimensions,
		"sourceQuality":        fragment.SourceQuality,
		"classificationJSON":   classificationJSON,
		"ownerProfileId":       fragment.OwnerProfileID,
		"ownerProfileName":     fragment.OwnerProfileName,
		"createdByProfileId":   fragment.CreatedByProfileID,
		"createdByProfileName": fragment.CreatedByProfileName,
		"createdAt":            fragment.CreatedAt,
		"updatedAt":            fragment.UpdatedAt,
	}

	_, err = s.writer.ScopedWrite(ctx, profileID, query, params)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("fragment create: persist failed",
				slog.String("team_id", profileID),
				slog.String("fragment_id", fragment.FragmentID),
				slog.String("error", err.Error()),
			)
		}
		s.metrics.IncFragmentCreate("error")
		return nil, fmt.Errorf("failed to persist fragment: %w", err)
	}

	// Step 9: Record first-write consistency (AC-52)
	if s.consistency != nil {
		if err := s.consistency.RecordFirstWrite(ctx, model, dims); err != nil {
			// Log but don't fail - the fragment is already persisted
			if s.logger != nil {
				s.logger.Warn("failed to record first-write consistency",
					slog.String("team_id", profileID),
					slog.String("fragment_id", fragmentID),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	// Step 10: Emit audit event (AC-26)
	// Raw content is NOT included in audit payload.
	// CorrelationID threads the upstream X-Correlation-ID into the audit row
	// so operators can stitch create events to the originating request (AC-54).
	if s.audit != nil {
		auditEntry := AuditLogEntry{
			ProfileID:     &profileID,
			Operation:     "fragment.create",
			EntityType:    "fragment",
			EntityID:      fragmentID,
			CorrelationID: correlation.FromContext(ctx),
			AfterPayload: map[string]interface{}{
				"fragment_id":          fragmentID,
				"team_id":              profileID,
				"source_type":          string(fragment.SourceType),
				"content_hash":         fragment.ContentHash,
				"embedding_model":      fragment.EmbeddingModel,
				"embedding_dimensions": fragment.EmbeddingDimensions,
				// content and embedding intentionally NOT included (AC-26)
			},
		}
		if err := s.audit.Append(ctx, auditEntry); err != nil {
			// Log but don't fail - the fragment is already persisted
			if s.logger != nil {
				s.logger.Warn("failed to emit audit event for fragment creation",
					slog.String("team_id", profileID),
					slog.String("fragment_id", fragmentID),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	s.metrics.IncFragmentCreate("created")
	return &CreateResult{
		Fragment:  fragment,
		Duplicate: false,
	}, nil
}

func validateCreateFragmentRequest(req *dto.CreateFragmentRequest) error {
	if req == nil {
		return errors.New("validation: fragment request is required")
	}
	if err := httpvalidation.ValidateStruct(req); err != nil {
		return fmt.Errorf("validation: %w", err)
	}
	if err := dto.ValidateMetadataSize(req.Metadata); err != nil {
		return fmt.Errorf("validation: %w", err)
	}
	return nil
}
