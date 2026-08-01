package memoryservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const rememberCheckAfterSeconds = 60

var (
	ErrRememberAuthContext = errors.New("remember: authenticated actor context is required")
	ErrRememberCredential  = errors.New("remember: authenticated credential context is required")
	ErrRememberConflict    = errors.New("remember: conflict")
	ErrRememberPersistence = errors.New("remember: persistence failed")
)

type RememberService interface {
	Remember(ctx context.Context, req RememberRequest) (*RememberResult, error)
	GetSubmissionStatus(ctx context.Context, req GetSubmissionStatusRequest) (*SubmissionStatusResult, error)
}

type RememberDependencies struct {
	Submissions repository.SubmissionRepository
	Metrics     observability.DiscoverabilityMetrics
}

type rememberService struct {
	submissions repository.SubmissionRepository
	metrics     observability.DiscoverabilityMetrics
}

func NewRememberService(deps RememberDependencies) RememberService {
	metrics := deps.Metrics
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &rememberService{submissions: deps.Submissions, metrics: metrics}
}

type RememberRequest struct {
	ContractVersion                 string                  `json:"contract_version"`
	Evidence                        []RememberEvidenceInput `json:"evidence"`
	EntityHints                     []map[string]any        `json:"entity_hints,omitempty"`
	RelationshipHints               []map[string]any        `json:"relationship_hints,omitempty"`
	ReplacesQuarantinedSubmissionID string                  `json:"replaces_quarantined_submission_id,omitempty"`
	IdempotencyKey                  string                  `json:"idempotency_key,omitempty"`
}

type GetSubmissionStatusRequest struct {
	ContractVersion string `json:"contract_version"`
	SubmissionID    string `json:"submission_id"`
}

type RememberEvidenceInput struct {
	Content                string         `json:"content"`
	SourceType             string         `json:"source_type,omitempty"`
	Source                 string         `json:"source,omitempty"`
	SourceGroup            string         `json:"source_group,omitempty"`
	Authority              string         `json:"authority,omitempty"`
	SourceKey              string         `json:"source_key,omitempty"`
	SourceRevision         string         `json:"source_revision,omitempty"`
	PreviousSourceRevision string         `json:"previous_source_revision,omitempty"`
	SupersedesEvidenceIDs  []string       `json:"supersedes_evidence_ids,omitempty"`
	IdempotencyKey         string         `json:"idempotency_key,omitempty"`
	Labels                 []string       `json:"labels,omitempty"`
	Metadata               map[string]any `json:"metadata,omitempty"`
}

type RememberResult struct {
	SubmissionID      string `json:"submission_id"`
	ProcessingState   string `json:"processing_state"`
	CheckAfterSeconds int    `json:"check_after_seconds"`
	StatusTool        string `json:"status_tool"`
	CorrelationID     string `json:"correlation_id"`
}

type SubmissionStatusResult struct {
	SubmissionID         string                                     `json:"submission_id"`
	ProcessingState      string                                     `json:"processing_state"`
	SearchState          string                                     `json:"search_state"`
	Evidence             []repository.SubmissionEvidenceStatus      `json:"evidence"`
	RelationshipOutcomes []repository.SubmissionRelationshipOutcome `json:"relationship_outcomes"`
	Errors               []repository.SubmissionStatusError         `json:"errors"`
	QuarantineExpiresAt  *time.Time                                 `json:"quarantine_expires_at,omitempty"`
}

func (s *rememberService) Remember(ctx context.Context, req RememberRequest) (*RememberResult, error) {
	started := time.Now()
	result, err := s.rememberSubmission(ctx, req)
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	observability.RecordRememberAcknowledgement(ctx, s.metrics, time.Since(started), outcome)
	return result, err
}

func sourceRevisionContentHashes(evidence []RememberEvidenceInput) map[string]string {
	groups := make(map[string][]string)
	for _, item := range evidence {
		key := sourceRevisionBatchKey(item)
		if key == "" {
			continue
		}
		groups[key] = append(groups[key], item.Content)
	}
	hashes := make(map[string]string, len(groups))
	for key, items := range groups {
		hashes[key] = sourceRevisionBatchHash(items)
	}
	return hashes
}

func sourceRevisionBatchKey(item RememberEvidenceInput) string {
	sourceKey := strings.TrimSpace(item.SourceKey)
	revision := strings.TrimSpace(item.SourceRevision)
	if sourceKey == "" || revision == "" {
		return ""
	}
	return sourceKey + "\x00" + revision + "\x00" + strings.TrimSpace(item.PreviousSourceRevision)
}

func sourceRevisionBatchHash(contents []string) string {
	h := sha256.New()
	for _, content := range contents {
		_, _ = fmt.Fprintf(h, "%d:", len(content))
		_, _ = h.Write([]byte(content))
		_, _ = h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func canonicalRequestHash(req RememberRequest) (string, error) {
	payload := map[string]any{
		"contract_version":                   req.ContractVersion,
		"evidence":                           req.Evidence,
		"entity_hints":                       req.EntityHints,
		"relationship_hints":                 req.RelationshipHints,
		"replaces_quarantined_submission_id": strings.TrimSpace(req.ReplacesQuarantinedSubmissionID),
		"idempotency_key":                    strings.TrimSpace(req.IdempotencyKey),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("remember: canonical request hash: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func evidenceSourceType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "conversation"
	}
	return value
}

func ledgerAuthorityAndMetadata(authority string, metadata map[string]any) (string, map[string]any) {
	out := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		out[key] = value
	}
	authority = strings.TrimSpace(authority)
	if authority != "" {
		out["contract_authority"] = authority
	}
	if authority == "" {
		return string(domain.AuthorityPrimary), out
	}
	if domain.Authority(authority).IsValid() {
		return authority, out
	}
	return authority, out
}

func evidenceProcessingIntentMetadata(metadata map[string]any, item RememberEvidenceInput) map[string]any {
	if len(item.SupersedesEvidenceIDs) > 0 {
		metadata["supersedes_evidence_ids"] = append([]string(nil), item.SupersedesEvidenceIDs...)
	}
	if value := strings.TrimSpace(item.IdempotencyKey); value != "" {
		metadata["evidence_idempotency_key"] = value
	}
	if value := strings.TrimSpace(item.SourceGroup); value != "" {
		metadata["contract_source_group"] = value
	}
	return metadata
}

func sourceRevisionEnvelope(item RememberEvidenceInput) map[string]any {
	return map[string]any{
		"source_type":  item.SourceType,
		"source":       item.Source,
		"source_group": item.SourceGroup,
		"metadata":     item.Metadata,
	}
}

func sourceSummary(evidence []RememberEvidenceInput) string {
	for _, item := range evidence {
		if value := strings.TrimSpace(item.SourceKey); value != "" {
			return value
		}
		if value := strings.TrimSpace(item.Source); value != "" {
			return value
		}
	}
	return fmt.Sprintf("remember evidence_count=%d", len(evidence))
}
