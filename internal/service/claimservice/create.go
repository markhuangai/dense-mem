package claimservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/claimidentity"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// claimDedupeLookup is the minimal interface for claim deduplication checks.
//
// Profile isolation invariant: every method MUST scope its query to profileID.
// Returning data from a different profile is a security violation.
type claimDedupeLookup interface {
	// ByIdempotencyKey returns the existing claim for profileID + key,
	// or (nil, nil) on a miss.
	ByIdempotencyKey(ctx context.Context, profileID, key string) (*domain.Claim, error)

	// ByContentHash returns the existing claim for profileID + hash,
	// or (nil, nil) on a miss.
	ByContentHash(ctx context.Context, profileID, hash string) (*domain.Claim, error)
}

// claimWriter is the minimal interface for persisting Claim nodes.
// It is satisfied by the ProfileScopeEnforcer returned by
// storage/neo4j.NewProfileScopeEnforcer — callers inject that value
// so no additional wiring is required.
type claimWriter interface {
	ScopedWrite(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, error)
}

// createClaimServiceImpl implements CreateClaimService.
type createClaimServiceImpl struct {
	lookup  claimDedupeLookup
	reader  supportedFragmentsReader
	writer  claimWriter
	audit   AuditEmitter
	logger  *slog.Logger
	metrics observability.DiscoverabilityMetrics
}

// Compile-time check that createClaimServiceImpl satisfies CreateClaimService.
var _ CreateClaimService = (*createClaimServiceImpl)(nil)

// NewCreateClaimService constructs a ready-to-use CreateClaimService.
//
// audit, logger, and metrics may be nil; audit failures are swallowed so the
// primary operation always succeeds, an absent logger emits no structured log
// lines, and absent metrics are silently skipped.
func NewCreateClaimService(
	lookup claimDedupeLookup,
	reader supportedFragmentsReader,
	writer claimWriter,
	audit AuditEmitter,
	logger *slog.Logger,
	metrics observability.DiscoverabilityMetrics,
) CreateClaimService {
	return &createClaimServiceImpl{
		lookup:  lookup,
		reader:  reader,
		writer:  writer,
		audit:   audit,
		logger:  logger,
		metrics: metrics,
	}
}

// createClaimCypher persists a Claim node and one SUPPORTED_BY edge per
// supporting fragment in a single atomic write.
//
// MERGE on (team_id, claim_id) makes the write race-safe: concurrent
// requests that derive the same deterministic claim_id will converge on one
// node rather than producing duplicates.  ON CREATE SET populates all fields
// only when the node is first written — a matched (already-existing) node is
// left untouched.
//
// UNWIND $edges creates one SUPPORTED_BY relationship per fragment entry.
// Each edge carries team_id and fragment_id for isolation enforcement and
// fast index scans, plus extracted_at and extract_conf as provenance metadata.
// When $edges is empty the UNWIND produces zero rows and no edges are written,
// but the Claim node MERGE has already committed.
//
// Profile isolation: $profileId is injected automatically by ScopedWrite.
// Callers MUST NOT include profileId in the params map.
const createClaimCypher = `
MERGE (c:Claim {team_id: $profileId, claim_id: $claimId})
ON CREATE SET
    c.subject                        = $subject,
    c.predicate                      = $predicate,
    c.object                         = $object,
    c.modality                       = $modality,
    c.polarity                       = $polarity,
    c.speaker                        = $speaker,
    c.span_start                     = $spanStart,
    c.span_end                       = $spanEnd,
    c.valid_from                     = $validFrom,
    c.valid_to                       = $validTo,
    c.recorded_at                    = $recordedAt,
    c.extract_conf                   = $extractConf,
    c.resolution_conf                = $resolutionConf,
    c.source_quality                 = $sourceQuality,
    c.entailment_verdict             = $entailmentVerdict,
    c.status                         = $status,
    c.extraction_model               = $extractionModel,
    c.extraction_version             = $extractionVersion,
    c.pipeline_run_id                = $pipelineRunId,
    c.content_hash                   = $contentHash,
    c.idempotency_key                = $idempotencyKey,
    c.classification_json            = $classificationJSON,
    c.classification_lattice_version = $classificationLatticeVersion,
    c.owner_profile_id               = $ownerProfileId,
    c.owner_profile_name             = $ownerProfileName,
    c.created_by_profile_id          = $createdByProfileId,
    c.created_by_profile_name        = $createdByProfileName
WITH c
UNWIND $edges AS edge
MATCH (sf:SourceFragment {team_id: $profileId, fragment_id: edge.fragment_id})
MERGE (c)-[r:SUPPORTED_BY {team_id: $profileId, fragment_id: edge.fragment_id}]->(sf)
ON CREATE SET
    r.extracted_at = edge.extracted_at,
    r.extract_conf = edge.extract_conf,
    r.speaker = edge.speaker,
    r.span_start = edge.span_start,
    r.span_end = edge.span_end,
    r.extraction_model = edge.extraction_model,
    r.extraction_version = edge.extraction_version,
    r.pipeline_run_id = edge.pipeline_run_id,
    r.authority = edge.authority`

// Create persists a new claim scoped to profileID.
//
// Algorithm:
//  1. Pre-hash field-length guard (ValidateClaimIdentityInputs).
//  2. Compute content_hash (SHA-256 of subject|predicate|object|valid_from).
//  3. Derive deterministic claim_id:
//     – UUIDv5(profileID, idempotencyKey) when key present, else
//     – UUIDv5(profileID, contentHash).
//  4. Deduplicate by idempotency_key.
//  5. Deduplicate by content_hash (only when no idempotency key).
//  6. Load supporting fragments (active-only) and compute quality signals.
//  7. Compute defaults:
//     status = "candidate"
//     entailment_verdict = "insufficient"
//     recorded_at = now (UTC)
//     source_quality = max(fragment.source_quality)
//     classification = lattice.Max(fragment.classification...)
//     classification_lattice_version = "v1"
//  8. Persist via ScopedWrite.
//  9. Emit audit event (failure swallowed).
func (s *createClaimServiceImpl) Create(ctx context.Context, profileID string, claim *domain.Claim) (*CreateResult, error) {
	if err := validateCreateClaimInput(claim); err != nil {
		return nil, fmt.Errorf("claim create: validation: %w", err)
	}

	// Step 1: pre-hash field-length guard.
	if err := claimidentity.ValidateClaimIdentityInputs(
		profileID,
		claim.Subject,
		claim.Predicate,
		claim.Object,
		claim.IdempotencyKey,
	); err != nil {
		return nil, fmt.Errorf("claim create: validation: %w", err)
	}

	// Step 2: compute content_hash.
	contentHash := claimidentity.ContentHash(claim.Subject, claim.Predicate, claim.Object, claim.ValidFrom)
	ownerID, ownerName, _ := requestctx.ActorOwner(ctx)
	identityScopeID := profileID
	if ownerID != "" {
		identityScopeID = ownerID
	}

	// Step 3: derive deterministic claim_id.
	var (
		claimID string
		idErr   error
	)
	if claim.IdempotencyKey != "" {
		claimID, idErr = claimidentity.ClaimID(identityScopeID, claim.IdempotencyKey)
	} else {
		claimID, idErr = claimidentity.ClaimIDFromHash(identityScopeID, contentHash)
	}
	if idErr != nil {
		return nil, fmt.Errorf("claim create: derive claim_id: %w", idErr)
	}

	// Step 4: deduplicate by idempotency_key.
	if claim.IdempotencyKey != "" {
		existing, err := s.lookup.ByIdempotencyKey(ctx, profileID, claim.IdempotencyKey)
		if err != nil {
			return nil, fmt.Errorf("claim create: idempotency lookup: %w", err)
		}
		if existing != nil {
			observability.RecordClaimCreate(ctx, s.metrics, "duplicate", "idempotency_key")
			return &CreateResult{
				Claim:       existing,
				Duplicate:   true,
				DuplicateOf: existing.ClaimID,
			}, nil
		}
	} else {
		// Step 5: deduplicate by content_hash (only when no idempotency key).
		existing, err := s.lookup.ByContentHash(ctx, profileID, contentHash)
		if err != nil {
			return nil, fmt.Errorf("claim create: content-hash lookup: %w", err)
		}
		if existing != nil {
			observability.RecordClaimCreate(ctx, s.metrics, "duplicate", "content_hash")
			return &CreateResult{
				Claim:       existing,
				Duplicate:   true,
				DuplicateOf: existing.ClaimID,
			}, nil
		}
	}

	// Step 6: load supporting fragments (active-only).
	support, err := loadSupportingFragments(ctx, s.reader, profileID, claim.SupportedBy)
	if err != nil {
		return nil, fmt.Errorf("claim create: %w", err)
	}

	// Step 7: compute defaults.
	now := time.Now().UTC()

	// Merged classification is map[string]string from the lattice; convert to
	// map[string]any because domain.Claim.Classification is typed that way.
	mergedClass := make(map[string]any, len(support.MergedClassification))
	for k, v := range support.MergedClassification {
		mergedClass[k] = v
	}

	newClaim := &domain.Claim{
		ClaimID:              claimID,
		ProfileID:            profileID,
		OwnerProfileID:       ownerID,
		OwnerProfileName:     ownerName,
		CreatedByProfileID:   ownerID,
		CreatedByProfileName: ownerName,
		// Semantic triple from the caller.
		Subject:   claim.Subject,
		Predicate: claim.Predicate,
		Object:    claim.Object,
		// Linguistic metadata from the caller.
		Modality:  claim.Modality,
		Polarity:  claim.Polarity,
		Speaker:   claim.Speaker,
		SpanStart: claim.SpanStart,
		SpanEnd:   claim.SpanEnd,
		// Temporal bounds from the caller.
		ValidFrom: claim.ValidFrom,
		ValidTo:   claim.ValidTo,
		// Pipeline defaults.
		RecordedAt:        now,
		Status:            domain.StatusCandidate,
		EntailmentVerdict: domain.EntailmentVerdict("insufficient"),
		// Confidence signals from the caller.
		ExtractConf:    claim.ExtractConf,
		ResolutionConf: claim.ResolutionConf,
		// Quality signals derived from supporting fragments.
		SourceQuality: support.MaxSourceQuality,
		// Provenance from the caller.
		ExtractionModel:   claim.ExtractionModel,
		ExtractionVersion: claim.ExtractionVersion,
		PipelineRunID:     claim.PipelineRunID,
		// Idempotency.
		ContentHash:    contentHash,
		IdempotencyKey: claim.IdempotencyKey,
		// Classification computed via lattice.
		Classification: mergedClass,
		// "v1" is the canonical lattice version. DefaultLattice (consumed by
		// support.go in this package) is built against this schema version.
		ClassificationLatticeVersion: "v1",
		// Supporting fragment IDs.
		SupportedBy: claim.SupportedBy,
	}
	newClaim.Evidence = make([]domain.Evidence, 0, len(support.Fragments))
	classificationJSON, err := fragmentcodec.EncodeOptionalMap(newClaim.Classification)
	if err != nil {
		observability.RecordClaimCreate(ctx, s.metrics, "error", "")
		return nil, fmt.Errorf("claim create: encode classification: %w", err)
	}

	// Step 8: persist to graph.
	//
	// Build one edge descriptor per supporting fragment. Each descriptor carries
	// the fields written onto the SUPPORTED_BY relationship:
	//   - fragment_id  — identifies the SourceFragment node for MATCH
	//   - extracted_at — set to recorded_at (the moment this claim was created)
	//   - extract_conf — confidence score from the extraction pipeline
	//
	// Profile isolation on the relationship is enforced by the $profileId
	// injection in ScopedWrite, which also populates the team_id property
	// written by the ON CREATE SET clause in createClaimCypher.
	edges := make([]map[string]any, 0, len(support.Fragments))
	for _, frag := range support.Fragments {
		evidence := domain.Evidence{
			FragmentID:        frag.FragmentID,
			Speaker:           newClaim.Speaker,
			SpanStart:         newClaim.SpanStart,
			SpanEnd:           newClaim.SpanEnd,
			ExtractConf:       newClaim.ExtractConf,
			ExtractionModel:   newClaim.ExtractionModel,
			ExtractionVersion: newClaim.ExtractionVersion,
			PipelineRunID:     newClaim.PipelineRunID,
			Authority:         frag.Authority,
		}
		newClaim.Evidence = append(newClaim.Evidence, evidence)
		edges = append(edges, map[string]any{
			"fragment_id":        frag.FragmentID,
			"extracted_at":       newClaim.RecordedAt,
			"extract_conf":       newClaim.ExtractConf,
			"speaker":            newClaim.Speaker,
			"span_start":         newClaim.SpanStart,
			"span_end":           newClaim.SpanEnd,
			"extraction_model":   newClaim.ExtractionModel,
			"extraction_version": newClaim.ExtractionVersion,
			"pipeline_run_id":    newClaim.PipelineRunID,
			"authority":          string(frag.Authority),
		})
	}

	params := map[string]any{
		"claimId":                      newClaim.ClaimID,
		"subject":                      newClaim.Subject,
		"predicate":                    newClaim.Predicate,
		"object":                       newClaim.Object,
		"modality":                     string(newClaim.Modality),
		"polarity":                     string(newClaim.Polarity),
		"speaker":                      newClaim.Speaker,
		"spanStart":                    newClaim.SpanStart,
		"spanEnd":                      newClaim.SpanEnd,
		"validFrom":                    newClaim.ValidFrom,
		"validTo":                      newClaim.ValidTo,
		"recordedAt":                   newClaim.RecordedAt,
		"extractConf":                  newClaim.ExtractConf,
		"resolutionConf":               newClaim.ResolutionConf,
		"sourceQuality":                newClaim.SourceQuality,
		"entailmentVerdict":            string(newClaim.EntailmentVerdict),
		"status":                       string(newClaim.Status),
		"extractionModel":              newClaim.ExtractionModel,
		"extractionVersion":            newClaim.ExtractionVersion,
		"pipelineRunId":                newClaim.PipelineRunID,
		"contentHash":                  newClaim.ContentHash,
		"idempotencyKey":               newClaim.IdempotencyKey,
		"classificationJSON":           classificationJSON,
		"classificationLatticeVersion": newClaim.ClassificationLatticeVersion,
		"ownerProfileId":               newClaim.OwnerProfileID,
		"ownerProfileName":             newClaim.OwnerProfileName,
		"createdByProfileId":           newClaim.CreatedByProfileID,
		"createdByProfileName":         newClaim.CreatedByProfileName,
		// edges drives the UNWIND ... MERGE for SUPPORTED_BY relationships.
		"edges": edges,
	}
	_, err = s.writer.ScopedWrite(ctx, profileID, createClaimCypher, params)
	if err != nil {
		observability.RecordClaimCreate(ctx, s.metrics, "error", "")
		return nil, fmt.Errorf("claim create: persist: %w", err)
	}

	observability.RecordClaimCreate(ctx, s.metrics, "created", "")

	// Step 9: emit audit event; swallow failures so the primary op succeeds.
	if s.audit != nil {
		entry := AuditLogEntry{
			ProfileID:  profileID,
			Timestamp:  now,
			Operation:  "claim.create",
			EntityType: "claim",
			EntityID:   claimID,
			// Raw text intentionally excluded from the audit payload.
			AfterPayload: map[string]any{
				"claim_id":     claimID,
				"team_id":      profileID,
				"content_hash": contentHash,
				"status":       string(domain.StatusCandidate),
			},
		}
		if auditErr := s.audit.Append(ctx, entry); auditErr != nil && s.logger != nil {
			s.logger.Warn("audit emit failed for claim.create",
				slog.String("team_id", profileID),
				slog.String("claim_id", claimID),
				slog.String("error", auditErr.Error()),
			)
		}
	}

	return &CreateResult{Claim: newClaim}, nil
}

func validateCreateClaimInput(claim *domain.Claim) error {
	if claim == nil {
		return errors.New("claim is required")
	}
	if claim.Modality != "" && !claim.Modality.IsValid() {
		return errors.New("modality must be one of [assertion question proposal speculation quoted]")
	}
	if claim.Polarity != "" && !claim.Polarity.IsValid() {
		return errors.New("polarity must be one of [+ -]")
	}
	if !validClaimConfidence(claim.ExtractConf) {
		return errors.New("extract_conf must be between 0 and 1")
	}
	if !validClaimConfidence(claim.ResolutionConf) {
		return errors.New("resolution_conf must be between 0 and 1")
	}
	if claim.ValidFrom != nil && claim.ValidTo != nil && claim.ValidFrom.After(*claim.ValidTo) {
		return errors.New("valid_from must not be after valid_to")
	}
	return nil
}

func validClaimConfidence(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
