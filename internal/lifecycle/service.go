package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/httperr"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

var ErrLifecycleAuthContext = errors.New("memory lifecycle: authenticated actor context is required")

type LifecycleService interface {
	RetractEvidence(context.Context, RetractEvidenceRequest) (*RetractEvidenceResult, error)
}
type Service = LifecycleService
type Dependencies = LifecycleDependencies
type LifecycleDependencies struct {
	Port knowledgecontract.LifecyclePort
}
type lifecycleService struct {
	port knowledgecontract.LifecyclePort
}

func New(deps Dependencies) Service { return NewLifecycleService(deps) }
func NewLifecycleService(deps LifecycleDependencies) LifecycleService {
	return &lifecycleService{port: deps.Port}
}

type RetractEvidenceRequest struct {
	EvidenceIDs    []string `json:"evidence_ids"`
	Reason         string   `json:"reason"`
	IdempotencyKey string   `json:"idempotency_key"`
}
type RetractEvidenceResult struct {
	DecisionID           string   `json:"decision_id"`
	ProcessingState      string   `json:"processing_state"`
	RetractedEvidenceIDs []string `json:"retracted_evidence_ids"`
}

func (s *lifecycleService) RetractEvidence(
	ctx context.Context,
	req RetractEvidenceRequest,
) (*RetractEvidenceResult, error) {
	if s.port == nil {
		return nil, errors.New("memory lifecycle: evidence repository is required")
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return nil, ErrLifecycleAuthContext
	}
	requestHash, err := retractEvidenceRequestHash(req)
	if err != nil {
		return nil, err
	}
	result, err := s.port.RetractEvidence(ctx, knowledgecontract.RetractEvidenceInput{
		TeamID:         actor.TeamID.String(),
		OwnerProfileID: actor.OwnerID.String(),
		EvidenceIDs:    append([]string(nil), req.EvidenceIDs...),
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
		RequestHash:    requestHash,
	})
	if err != nil {
		return nil, translateEvidenceLifecycleError(err)
	}
	return &RetractEvidenceResult{
		DecisionID:           result.DecisionID,
		ProcessingState:      result.ProcessingState,
		RetractedEvidenceIDs: append([]string(nil), result.RetractedEvidenceIDs...),
	}, nil
}

// Retract's unchanged request contract must replay hashes written before v2.6.
const retractEvidenceRequestHashContractVersion = "dense-mem.v2.4"

func retractEvidenceRequestHash(req RetractEvidenceRequest) (string, error) {
	evidenceIDs := make([]string, len(req.EvidenceIDs))
	for index, evidenceID := range req.EvidenceIDs {
		evidenceIDs[index] = strings.TrimSpace(evidenceID)
	}
	sort.Strings(evidenceIDs)
	payload, err := json.Marshal(map[string]any{
		"contract_version": retractEvidenceRequestHashContractVersion,
		"evidence_ids":     evidenceIDs,
		"reason":           strings.TrimSpace(req.Reason),
		"idempotency_key":  strings.TrimSpace(req.IdempotencyKey),
	})
	if err != nil {
		return "", fmt.Errorf("memory lifecycle: canonical retract request hash: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func translateEvidenceLifecycleError(err error) error {
	switch {
	case errors.Is(err, knowledgecontract.ErrEvidenceLifecycleNotFound), errors.Is(err, knowledgecontract.ErrTeamInactive):
		return httperr.New(httperr.NOT_FOUND, "evidence not found")
	case errors.Is(err, knowledgecontract.ErrIdempotencyConflict):
		return httperr.WithGuidance(httperr.New(httperr.CONFLICT, "evidence lifecycle conflict"), "idempotency_conflict", "correct_and_resubmit", "Use a new idempotency key for the changed retraction request, then submit again.", false, nil, "")
	case errors.Is(err, knowledgecontract.ErrEvidenceLifecycleConflict):
		return httperr.WithGuidance(httperr.New(httperr.CONFLICT, "evidence lifecycle conflict"), "evidence_lifecycle_conflict", "refresh_state", "Refresh the current evidence state, then submit the retraction again with a new idempotency key.", false, nil, "")
	default:
		return err
	}
}
